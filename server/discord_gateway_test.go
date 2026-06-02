package main

// Discord Gateway client tests (v5.52.0).
//
// All tests run against an in-process httptest server playing the
// gateway's role; no real network. The handler closures capture the
// shape of each request and the canned response so individual cases
// can assert on specific verbs / paths / headers without spinning up
// a full mock server per test.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// gwStub is a tiny in-process gateway useful as a router-style
// httptest backend. Each path returns whatever the handler stuffed
// into it; tests can read recorded values to confirm the client
// sent the right thing.
type gwStub struct {
	*httptest.Server
	mux       *http.ServeMux
	pairCalls int32
	pollCalls int32
	hzCalls   int32
	sendCalls int32
}

func newGwStub() *gwStub {
	mux := http.NewServeMux()
	s := &gwStub{mux: mux}
	s.Server = httptest.NewTLSServer(mux)
	return s
}

// configure points the store's discordGateway at this stub's URL.
// Returns the populated config so the test can mutate further.
func (g *gwStub) wire(t *testing.T, store *Store) *models.DiscordGateway {
	t.Helper()
	// httptest URL looks like https://127.0.0.1:NNNN — peel into the
	// host/port the dgEndpoint helper expects.
	u := g.URL
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	parts := strings.Split(u, ":")
	if len(parts) != 2 {
		t.Fatalf("bad stub URL: %s", g.URL)
	}
	cfg := &models.DiscordGateway{
		Enabled:   true,
		Host:      parts[0],
		Port:      atoi(parts[1]),
		TLS:       strings.HasPrefix(g.URL, "https"),
		VerifyTLS: false, // stub uses self-signed
	}
	store.SetDiscordGateway(cfg)
	return cfg
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// ── tests ────────────────────────────────────────────────────────

func TestDG_PairRequest_StoresRegistrationID(t *testing.T) {
	stub := newGwStub()
	defer stub.Close()
	stub.mux.HandleFunc("/pair/request", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stub.pairCalls, 1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["source_kind"] != "kvm-center" {
			t.Errorf("source_kind: got %q", body["source_kind"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"reg_xyz","status":"pending"}`))
	})

	store := newTestStore(t)
	stub.wire(t, store)

	c := newGatewayClient(store)
	if err := c.pairRequest(context.Background(), map[string]string{"source_kind": "kvm-center"}); err != nil {
		t.Fatal(err)
	}
	got := store.discordGatewaySnapshot()
	if got.RegistrationID != "reg_xyz" {
		t.Errorf("RegistrationID: got %q", got.RegistrationID)
	}
	if got.Status != dgStatusPending {
		t.Errorf("Status: got %q", got.Status)
	}
}

func TestDG_PollStatus_PendingReturnsPending(t *testing.T) {
	stub := newGwStub()
	defer stub.Close()
	stub.mux.HandleFunc("/pair/status/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"reg_xyz","status":"pending"}`))
	})

	store := newTestStore(t)
	stub.wire(t, store)
	store.patchDiscordGateway(func(g *models.DiscordGateway) {
		g.RegistrationID = "reg_xyz"
		g.Status = dgStatusPending
	})
	c := newGatewayClient(store)
	got, err := c.pollStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != dgStatusPending {
		t.Errorf("status: got %q", got)
	}
}

func TestDG_PollStatus_ApprovedCapturesToken(t *testing.T) {
	stub := newGwStub()
	defer stub.Close()
	stub.mux.HandleFunc("/pair/status/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"reg_xyz","status":"approved","webhook_id":"kvmc-prod","token":"tok_secret"}`))
	})
	store := newTestStore(t)
	stub.wire(t, store)
	store.patchDiscordGateway(func(g *models.DiscordGateway) {
		g.RegistrationID = "reg_xyz"
		g.Status = dgStatusPending
	})
	c := newGatewayClient(store)
	if _, err := c.pollStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := store.discordGatewaySnapshot()
	if got.WebhookID != "kvmc-prod" || got.Token != "tok_secret" {
		t.Errorf("creds not captured: %+v", got)
	}
	if got.Status != dgStatusApproved {
		t.Errorf("status: got %q", got.Status)
	}
}

func TestDG_PollStatus_404FlipsToRevoked(t *testing.T) {
	stub := newGwStub()
	defer stub.Close()
	stub.mux.HandleFunc("/pair/status/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	store := newTestStore(t)
	stub.wire(t, store)
	store.patchDiscordGateway(func(g *models.DiscordGateway) {
		g.RegistrationID = "reg_gone"
		g.Status = dgStatusPending
	})
	c := newGatewayClient(store)
	if _, err := c.pollStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := store.discordGatewaySnapshot().Status; got != dgStatusRevoked {
		t.Errorf("status: got %q want revoked", got)
	}
}

func TestDG_Healthz_503MarksConnecting(t *testing.T) {
	stub := newGwStub()
	defer stub.Close()
	stub.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"discord_ready":false}`))
	})
	store := newTestStore(t)
	stub.wire(t, store)
	c := newGatewayClient(store)
	ok, ready, _ := c.healthz(context.Background())
	if ok || ready {
		t.Errorf("expected ok=false ready=false, got ok=%v ready=%v", ok, ready)
	}
}

func TestDG_Healthz_200MarksHealthy(t *testing.T) {
	stub := newGwStub()
	defer stub.Close()
	stub.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"discord_ready":true,"uptime_seconds":42}`))
	})
	store := newTestStore(t)
	stub.wire(t, store)
	c := newGatewayClient(store)
	ok, ready, err := c.healthz(context.Background())
	if err != nil || !ok || !ready {
		t.Errorf("ok=%v ready=%v err=%v", ok, ready, err)
	}
}

func TestDG_SendCard_204Success(t *testing.T) {
	stub := newGwStub()
	defer stub.Close()
	stub.mux.HandleFunc("/webhook/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stub.sendCalls, 1)
		if got := r.Header.Get("Authorization"); got != "Bearer tok_secret" {
			t.Errorf("auth header: got %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	store := newTestStore(t)
	stub.wire(t, store)
	store.patchDiscordGateway(func(g *models.DiscordGateway) {
		g.WebhookID = "kvmc-prod"
		g.Token = "tok_secret"
		g.Status = dgStatusHealthy
	})
	c := newGatewayClient(store)
	ok, err := c.sendCard(context.Background(), dgCard{Title: "hi"})
	if !ok || err != nil {
		t.Errorf("send: ok=%v err=%v", ok, err)
	}
	if atomic.LoadInt32(&stub.sendCalls) != 1 {
		t.Errorf("expected 1 send, got %d", stub.sendCalls)
	}
}

func TestDG_SendCard_401MarksRevoked(t *testing.T) {
	stub := newGwStub()
	defer stub.Close()
	stub.mux.HandleFunc("/webhook/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	store := newTestStore(t)
	stub.wire(t, store)
	store.patchDiscordGateway(func(g *models.DiscordGateway) {
		g.WebhookID = "kvmc-prod"
		g.Token = "wrong"
		g.Status = dgStatusHealthy
	})
	c := newGatewayClient(store)
	ok, err := c.sendCard(context.Background(), dgCard{Title: "hi"})
	if ok || err == nil {
		t.Error("expected error on 401")
	}
	if got := store.discordGatewaySnapshot().Status; got != dgStatusRevoked {
		t.Errorf("status after 401: got %q want revoked", got)
	}
}

func TestDG_SendCard_424LogsAndDoesNotRevoke(t *testing.T) {
	stub := newGwStub()
	defer stub.Close()
	stub.mux.HandleFunc("/webhook/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusFailedDependency)
	})
	store := newTestStore(t)
	stub.wire(t, store)
	store.patchDiscordGateway(func(g *models.DiscordGateway) {
		g.WebhookID = "kvmc-prod"
		g.Token = "tok"
		g.Status = dgStatusHealthy
	})
	c := newGatewayClient(store)
	ok, err := c.sendCard(context.Background(), dgCard{Title: "hi"})
	if ok || err == nil {
		t.Error("expected 424 to be reported as error")
	}
	// 424 is a gateway-config issue, not a kvm-center auth problem;
	// status must stay healthy so the next send retries naturally.
	if got := store.discordGatewaySnapshot().Status; got != dgStatusHealthy {
		t.Errorf("status after 424: got %q want healthy", got)
	}
}

func TestDG_PutHandler_FlipsToSavingAndKicksPair(t *testing.T) {
	stub := newGwStub()
	defer stub.Close()
	pairFired := make(chan struct{}, 1)
	stub.mux.HandleFunc("/pair/request", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case pairFired <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"reg_p","status":"pending"}`))
	})
	store := newTestStore(t)

	host := strings.TrimPrefix(stub.URL, "https://")
	parts := strings.Split(host, ":")
	body := `{"enabled":true,"host":"` + parts[0] + `","port":` + parts[1] + `,"tls":true,"verify_tls":false}`

	r := httptestPut("/api/settings/discord-gateway", body)
	w := httptestRecorder()
	handleDiscordGatewayPut(store)(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status: %d", w.Code)
	}

	select {
	case <-pairFired:
	case <-time.After(2 * time.Second):
		t.Fatal("pair-request was not fired by PUT handler")
	}

	// `go runPairRequest` is a non-saveWG goroutine: after pairFired
	// arrives the stub has replied but the goroutine still needs to
	// decode the response and call patchDiscordGateway → AsyncSave.
	// Wait for that side-effect (RegistrationID populated) before the
	// test exits — otherwise the AsyncSave fires after t.Cleanup runs
	// saveWG.Wait and races RemoveAll on t.TempDir.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := store.discordGatewaySnapshot(); got != nil && got.RegistrationID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got := store.discordGatewaySnapshot()
	if got == nil || !got.Enabled {
		t.Errorf("config not persisted: %+v", got)
	}
}

// ── audit firehose (v5.53.0) ─────────────────────────────────────

func TestDG_AuditFirehose_RespectsToggle(t *testing.T) {
	store := newTestStore(t)
	store.discordGwQueue = make(chan *models.NotificationEvent, 4)
	defer close(store.discordGwQueue)

	// Toggle off → should drop on the floor.
	store.SetDiscordGateway(&models.DiscordGateway{Enabled: true, Status: dgStatusHealthy})
	store.maybeShipAuditToDiscord(&models.AuditEvent{Action: "vm.create", Success: true})
	if got := len(store.discordGwQueue); got != 0 {
		t.Errorf("toggle off: queue should be empty, got %d", got)
	}

	// Toggle on, failed-only → success event should drop, fail should pass.
	store.SetDiscordGateway(&models.DiscordGateway{Enabled: true, Status: dgStatusHealthy, ShipAuditEvents: true, ShipAuditFailedOnly: true})
	store.maybeShipAuditToDiscord(&models.AuditEvent{Action: "vm.create", Success: true})
	if got := len(store.discordGwQueue); got != 0 {
		t.Errorf("failed-only + success: queue should be empty, got %d", got)
	}
	store.maybeShipAuditToDiscord(&models.AuditEvent{Action: "vm.delete", Success: false, Error: "boom"})
	if got := len(store.discordGwQueue); got != 1 {
		t.Errorf("failed-only + fail: expected 1 in queue, got %d", got)
	}
	<-store.discordGwQueue // drain

	// Toggle on, all events → success and fail both ship.
	store.SetDiscordGateway(&models.DiscordGateway{Enabled: true, Status: dgStatusHealthy, ShipAuditEvents: true, ShipAuditFailedOnly: false})
	store.maybeShipAuditToDiscord(&models.AuditEvent{Action: "vm.create", Success: true})
	store.maybeShipAuditToDiscord(&models.AuditEvent{Action: "vm.delete", Success: false})
	if got := len(store.discordGwQueue); got != 2 {
		t.Errorf("all events: expected 2 in queue, got %d", got)
	}
}

func TestDG_AuditToNotification_SeverityAndShape(t *testing.T) {
	ev := &models.AuditEvent{
		Action:    "vm.delete",
		Resource:  "vm/abc-123",
		ActorName: "alice",
		ActorIP:   "10.0.0.5",
		Success:   false,
		Error:     "permission denied",
		Detail:    map[string]string{"via": "token:xyz", "scope": "vm.delete"},
	}
	n := auditToNotification(ev)
	if n.Severity != "warn" {
		t.Errorf("severity: got %q want warn", n.Severity)
	}
	if n.Type != "audit.vm.delete" {
		t.Errorf("type: got %q", n.Type)
	}
	if n.Resource != "vm/abc-123" {
		t.Errorf("resource: got %q", n.Resource)
	}
	if !strings.Contains(n.Message, "alice") || !strings.Contains(n.Message, "permission denied") {
		t.Errorf("message missing actor/error: %q", n.Message)
	}
	// "via" should be filtered out, "scope" should land in detail.
	if _, ok := n.Detail["via"]; ok {
		t.Error("'via' should be excluded from card detail")
	}
	if n.Detail["scope"] != "vm.delete" {
		t.Errorf("scope detail not preserved: %v", n.Detail)
	}
}

// ── small httptest helpers ───────────────────────────────────────

func httptestPut(path, body string) *http.Request {
	r, _ := http.NewRequest(http.MethodPut, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	// authMiddleware is bypassed in this unit test — handler reads
	// userFromRequest only for audit attribution, which tolerates nil.
	return r
}

func httptestRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }
