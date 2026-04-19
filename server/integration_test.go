package main

// Integration-flow test harness (v5.27). Sits alongside the per-handler
// unit tests but drives multiple endpoints end-to-end as a real client
// would — register an admin, sign in, mint a token, use the token, etc.
// Lets us catch wiring regressions that single-handler tests miss.
//
// Scope of this file: the harness + helper assertions. Flow tests live
// in sibling integration_*_test.go files.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"kvm-center/internal/models"
)

// integrationHarness is a fully-wired in-process kvm-center server that
// tests can drive via real HTTP. Uses the production handler + middleware
// stack so auth, RBAC, and feature flags behave exactly as in prod.
type integrationHarness struct {
	t      *testing.T
	Store  *Store
	Server *httptest.Server
	Client *http.Client
}

// newIntegrationHarness boots a test server with a fresh in-memory store.
// Drains background AsyncSave goroutines during cleanup so t.TempDir
// teardown doesn't race with late renames. Uses a cookie jar so tests
// can follow the login → session-cookie → subsequent-request flow
// without manually threading Set-Cookie.
func newIntegrationHarness(t *testing.T) *integrationHarness {
	t.Helper()
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })

	mux := buildTestMux(s)
	handler := loggingMiddleware(authMiddleware(s, mux))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		// Don't auto-follow — tests want to see redirects.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &integrationHarness{t: t, Store: s, Server: ts, Client: client}
}

// buildTestMux wires the subset of routes the integration tests drive.
// Kept in lockstep with main.go's mux-building block; when routes move
// there, they move here too. (A future refactor can factor both into a
// shared helper.)
func buildTestMux(s *Store) *http.ServeMux {
	mux := http.NewServeMux()

	// Auth (public + session-scoped).
	mux.HandleFunc("/api/auth/status", handleAuthStatus(s))
	mux.HandleFunc("/api/auth/register", handleAuthRegister(s))
	mux.HandleFunc("/api/auth/login", handleAuthLogin(s))
	mux.HandleFunc("/api/auth/logout", handleAuthLogout(s))
	mux.HandleFunc("/api/auth/me", handleAuthMe(s))

	// Setup wizard (/state public; /complete admin-only).
	mux.HandleFunc("/api/setup/state", handleSetupState(s))
	mux.Handle("/api/setup/complete", requirePerm(s, "user.manage", http.HandlerFunc(handleSetupComplete(s))))
	mux.Handle("/api/settings/public-url", requirePerm(s, "user.manage", handlePublicURL(s)))

	// Settings / features.
	mux.HandleFunc("/api/settings", handleFeaturesGet(s))
	mux.Handle("/api/settings/update", requirePerm(s, "user.manage", handleFeaturesUpdate(s)))

	// Hosts + VMs + telemetry. DELETE on /api/hosts/{id} matches v5.26.0.
	mux.HandleFunc("/api/telemetry", handleTelemetry(s))
	mux.HandleFunc("/api/hosts", handleGetHosts(s))
	mux.HandleFunc("/api/hosts/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/maintenance/enter"):
			requirePerm(s, "host.manage", http.HandlerFunc(handleMaintenanceEnter(s))).ServeHTTP(w, r)
		case strings.HasSuffix(r.URL.Path, "/maintenance/exit"):
			requirePerm(s, "host.manage", http.HandlerFunc(handleMaintenanceExit(s))).ServeHTTP(w, r)
		case strings.HasSuffix(r.URL.Path, "/metrics"):
			handleHostMetrics(s).ServeHTTP(w, r)
		case r.Method == http.MethodDelete:
			requirePerm(s, "host.manage", http.HandlerFunc(handleDeleteHost(s))).ServeHTTP(w, r)
		default:
			handleGetHost(s).ServeHTTP(w, r)
		}
	})
	mux.HandleFunc("/api/vms", handleGetVMs(s))

	// Migrations.
	mux.HandleFunc("/api/migrations", handleGetMigrations(s))
	mux.HandleFunc("/api/migrations/record", handlePostMigration(s))

	// API tokens (admin-scoped + self-service).
	mux.Handle("/api/tokens", requirePerm(s, "user.manage", http.HandlerFunc(handleAPITokens(s))))
	mux.Handle("/api/tokens/me", http.HandlerFunc(handleMyAPITokens(s)))
	mux.Handle("/api/tokens/me/", http.HandlerFunc(handleMyAPIToken(s)))
	mux.Handle("/api/tokens/", requirePerm(s, "user.manage", http.HandlerFunc(handleAPIToken(s))))

	// Users (user.manage).
	mux.Handle("/api/users", requirePerm(s, "user.manage", handleUsersList(s)))
	mux.Handle("/api/users/new", requirePerm(s, "user.manage", handleUsersCreate(s)))
	mux.Handle("/api/users/", requirePerm(s, "user.manage", handleUserUpdate(s)))

	// Admin ops.
	mux.Handle("/api/admin/backup", requirePerm(s, "user.manage", handleBackup(s)))

	// Health.
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"` + Version + `"}`))
	})

	return mux
}

// ---------- HTTP helpers ------------------------------------------------

// do runs a request through the harness's Client and returns status +
// body. Any non-HTTP errors fail the test. Callers inspect the returned
// status + decode body themselves.
func (h *integrationHarness) do(method, path string, body any) (int, []byte) {
	h.t.Helper()
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal %s %s: %v", method, path, err)
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.Server.URL+path, buf)
	if err != nil {
		h.t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		h.t.Fatalf("request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// doJSON wraps do + JSON-decodes into dst. Returns the status code.
func (h *integrationHarness) doJSON(method, path string, body, dst any) int {
	h.t.Helper()
	status, out := h.do(method, path, body)
	if dst != nil && len(out) > 0 {
		if err := json.Unmarshal(out, dst); err != nil {
			h.t.Fatalf("decode %s %s (status=%d body=%s): %v", method, path, status, out, err)
		}
	}
	return status
}

// doWithToken runs a request carrying a Bearer token (not a cookie).
// Used by tests that need to verify token auth independently of the
// session cookie path.
func (h *integrationHarness) doWithToken(method, path, token string, body any) (int, []byte) {
	h.t.Helper()
	var buf io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.Server.URL+path, buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Dedicated client so we don't muddle the session-cookie jar.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := c.Do(req)
	if err != nil {
		h.t.Fatalf("request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// registerFirstAdmin uses /api/auth/register's "first user becomes
// admin" behaviour to seed a fully-privileged account and leave the
// harness logged in as that user.
func (h *integrationHarness) registerFirstAdmin(username, password string) {
	h.t.Helper()
	status, body := h.do("POST", "/api/auth/register", map[string]string{
		"username": username,
		"password": password,
	})
	if status != http.StatusOK {
		h.t.Fatalf("register admin %q: status=%d body=%s", username, status, body)
	}
}

// login authenticates and persists the session cookie in the harness
// jar. Returns the status so negative-path tests can assert failures.
func (h *integrationHarness) login(username, password string) int {
	h.t.Helper()
	status, _ := h.do("POST", "/api/auth/login", map[string]string{
		"username": username,
		"password": password,
	})
	return status
}

// mustLogin fails the test on anything other than 200.
func (h *integrationHarness) mustLogin(username, password string) {
	h.t.Helper()
	if status := h.login(username, password); status != http.StatusOK {
		h.t.Fatalf("login %q: status=%d", username, status)
	}
}

// seedUser creates a non-admin user directly in the store (bypasses the
// registration path which auto-elevates the first user). Returns the
// user record so tests can attach custom role lists, etc.
func (h *integrationHarness) seedUser(username, password string, roleIDs []string) *models.User {
	h.t.Helper()
	hashed, err := hashPassword(password)
	if err != nil {
		h.t.Fatalf("hash password for %q: %v", username, err)
	}
	u := &models.User{
		Username:     username,
		PasswordHash: hashed,
		Enabled:      true,
		RoleIDs:      roleIDs,
	}
	if err := h.Store.CreateUser(u); err != nil {
		h.t.Fatalf("seed user %q: %v", username, err)
	}
	return u
}

// ---------- Stub hypervisor agent --------------------------------------

// stubAgent imitates enough of the hypervisor-side HTTP interface that
// the server's command dispatcher + command responses look real. Tests
// configure which commands succeed, which fail, and capture the
// inbound requests for assertion.
type stubAgent struct {
	t           *testing.T
	Server      *httptest.Server
	mu          sync.Mutex
	commands    []stubAgentCommand
	nextStatus  int
	nextBody    []byte
	failCommand string // when set, all matching commands return 500
}

type stubAgentCommand struct {
	Action string
	VMName string
	Params map[string]string
}

func newStubAgent(t *testing.T) *stubAgent {
	t.Helper()
	sa := &stubAgent{t: t, nextStatus: http.StatusOK, nextBody: []byte(`{"success":true,"message":"ok"}`)}
	mux := http.NewServeMux()
	mux.HandleFunc("/command", sa.handleCommand)
	mux.HandleFunc("/migrate-status", sa.handleMigrateStatus)
	mux.HandleFunc("/cancel-migrate", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	sa.Server = httptest.NewServer(mux)
	t.Cleanup(sa.Server.Close)
	return sa
}

func (sa *stubAgent) handleCommand(w http.ResponseWriter, r *http.Request) {
	var req models.CommandRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	sa.mu.Lock()
	sa.commands = append(sa.commands, stubAgentCommand{
		Action: req.Action,
		VMName: req.VMName,
		Params: req.Params,
	})
	failMatch := sa.failCommand != "" && sa.failCommand == req.Action
	status := sa.nextStatus
	body := sa.nextBody
	sa.mu.Unlock()
	if failMatch {
		http.Error(w, "stub-agent: configured to fail action="+req.Action, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (sa *stubAgent) handleMigrateStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"vm_name":"","status":"","phase":"","progress":0}`))
}

// Commands returns a snapshot copy of the commands the stub has
// received, newest last.
func (sa *stubAgent) Commands() []stubAgentCommand {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	out := make([]stubAgentCommand, len(sa.commands))
	copy(out, sa.commands)
	return out
}

// ---------- small assertion helpers ------------------------------------

func assertStatus(t *testing.T, want, got int, context string) {
	t.Helper()
	if want != got {
		t.Fatalf("%s: want status %d, got %d", context, want, got)
	}
}
