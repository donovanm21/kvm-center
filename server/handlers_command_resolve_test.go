package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func resolutionActor(s *Store) *models.User {
	u := &models.User{ID: "resolver", Username: "resolver", Enabled: true, RoleIDs: []string{"resolution-role"}}
	s.mu.Lock()
	s.users[u.ID] = u
	s.roles["resolution-role"] = &models.Role{ID: "resolution-role", Permissions: []string{"user.manage"}}
	s.mu.Unlock()
	return u
}
func resolutionRequest(s *Store, u *models.User, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/admin/command-log/resolve", strings.NewReader(body))
	if u != nil {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
	}
	w := httptest.NewRecorder()
	commandResolutionRoute(s).ServeHTTP(w, r)
	return w
}

const resolutionBody = `{"request_id":"resolve-one","outcome":"success","note":"Observed expected state"}`

func seedResolution(s *Store) {
	s.commandLog.Record(&models.CommandLogEntry{RequestID: "resolve-one", Outcome: "unknown", SubmittedAt: time.Now(), HostID: "fixture-host"})
}
func assertResolution(t *testing.T, s *Store) {
	t.Helper()
	entries := s.commandLog.List(10)
	if len(entries) != 1 || entries[0].Outcome != "success" || entries[0].Resolution == nil || entries[0].Resolution.ActorID != "resolver" || entries[0].Resolution.Note != "Observed expected state" {
		t.Fatalf("resolution not preserved: %+v", entries)
	}
	if s.commandLog.Update("resolve-one", "failed", "late result", 500, false) {
		t.Fatal("late update overwrote resolution")
	}
}
func TestCommandResolutionDurableAndIdempotent(t *testing.T) {
	s := newTestStore(t)
	u := resolutionActor(s)
	seedResolution(s)
	for i := 0; i < 2; i++ {
		if w := resolutionRequest(s, u, resolutionBody); w.Code != 200 {
			t.Fatalf("resolve: %d %s", w.Code, w.Body)
		}
	}
	events, _ := s.GetAuditEvents(AuditFilter{})
	count := 0
	for _, e := range events {
		if e.Action == "command.resolved" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("resolution audit count=%d", count)
	}
	s.saveWG.Wait()
	loaded := NewStore(s.dataFile)
	t.Cleanup(func() { loaded.saveWG.Wait() })
	assertResolution(t, loaded)
	if w := resolutionRequest(loaded, u, resolutionBody); w.Code != 200 {
		t.Fatalf("retry after reload: %d", w.Code)
	}
	for _, body := range []string{strings.Replace(resolutionBody, "success", "failed", 1), strings.Replace(resolutionBody, "expected", "different", 1)} {
		if w := resolutionRequest(s, u, body); w.Code != 409 {
			t.Fatalf("conflict: %d", w.Code)
		}
	}
}
func TestCommandResolutionValidationAndState(t *testing.T) {
	s := newTestStore(t)
	u := resolutionActor(s)
	seedResolution(s)
	if w := resolutionRequest(s, nil, resolutionBody); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if w := resolutionRequest(s, &models.User{ID: "ordinary", Enabled: true}, resolutionBody); w.Code != 403 {
		t.Fatalf("permission: %d", w.Code)
	}
	for _, body := range []string{`{}`, resolutionBody + ` {}`, strings.Replace(resolutionBody, "success", "pending", 1), strings.Replace(resolutionBody, "Observed expected state", "", 1)} {
		if w := resolutionRequest(s, u, body); w.Code != 400 {
			t.Fatalf("invalid request: %d", w.Code)
		}
	}
	if w := resolutionRequest(s, u, strings.Replace(resolutionBody, "resolve-one", "missing", 1)); w.Code != 404 {
		t.Fatalf("missing: %d", w.Code)
	}
	for _, state := range []string{"pending", "success", "failed", "cancelled"} {
		s.commandLog.Update("resolve-one", state, "", 0, false)
		if w := resolutionRequest(s, u, resolutionBody); w.Code != 409 {
			t.Fatalf("state %s: %d", state, w.Code)
		}
	}
}
func TestCommandResolutionConcurrent(t *testing.T) {
	s := newTestStore(t)
	u := resolutionActor(s)
	seedResolution(s)
	results := make(chan int, 2)
	for _, body := range []string{resolutionBody, strings.Replace(resolutionBody, "success", "failed", 1)} {
		go func(body string) { results <- resolutionRequest(s, u, body).Code }(body)
	}
	a, b := <-results, <-results
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatalf("competing resolutions: %d %d", a, b)
	}
}

// Wrap a real JSON backend so errors and blocked acknowledgements do not
// bypass the actual persistence snapshot/serialization path.
type resolutionBackend struct {
	Backend
	fail    atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (b *resolutionBackend) Save(ctx context.Context, d *persistData) error {
	if b.fail.Load() {
		return errors.New("synthetic persistence failure")
	}
	if b.entered != nil {
		select {
		case b.entered <- struct{}{}:
		default:
		}
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.Backend.Save(ctx, d)
}
func TestCommandResolutionSaveFailureRetry(t *testing.T) {
	s := newTestStore(t)
	u := resolutionActor(s)
	seedResolution(s)
	s.saveWG.Wait()
	b := &resolutionBackend{Backend: s.backend}
	s.backend = b
	b.fail.Store(true)
	if w := resolutionRequest(s, u, resolutionBody); w.Code != 503 {
		t.Fatalf("failed save: %d", w.Code)
	}
	assertResolution(t, s)
	b.fail.Store(false)
	if w := resolutionRequest(s, u, resolutionBody); w.Code != 200 {
		t.Fatalf("retry: %d", w.Code)
	}
	s.saveWG.Wait()
	loaded := NewStore(s.dataFile)
	t.Cleanup(func() { loaded.saveWG.Wait() })
	assertResolution(t, loaded)
}
func TestCommandResolutionWaitsForPersistence(t *testing.T) {
	s := newTestStore(t)
	u := resolutionActor(s)
	seedResolution(s)
	s.saveWG.Wait()
	b := &resolutionBackend{Backend: s.backend, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.backend = b
	done := make(chan int, 1)
	go func() { done <- resolutionRequest(s, u, resolutionBody).Code }()
	<-b.entered
	select {
	case code := <-done:
		close(b.release)
		t.Fatalf("acknowledged before save: %d", code)
	default:
	}
	close(b.release)
	if code := <-done; code != 200 {
		t.Fatalf("save acknowledgement: %d", code)
	}
}

type resolutionDoer func(*http.Request) (*http.Response, error)

func (f resolutionDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }
func TestCommandResolutionLateReconciler(t *testing.T) {
	s := newTestStore(t)
	u := resolutionActor(s)
	s.hub = NewSSEHub()
	s.hosts["fixture-host"] = &models.Host{ID: "fixture-host", IP: "127.0.0.1", AgentPort: 8081}
	entry, _ := s.commandLog.Record(&models.CommandLogEntry{RequestID: "resolve-one", Outcome: "pending", HostID: "fixture-host", SubmittedAt: time.Now()})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	doer := resolutionDoer(func(*http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"outcome":"failed","message":"late failure"}`))}, nil
	})
	go func() { reconcileOne(context.Background(), s, doer, entry); close(done) }()
	<-entered
	s.commandLog.Update("resolve-one", "unknown", "undetermined", 0, false)
	code := resolutionRequest(s, u, resolutionBody).Code
	close(release)
	<-done
	if code != 200 {
		t.Fatalf("resolution: %d", code)
	}
	assertResolution(t, s)
	events, _ := s.GetAuditEvents(AuditFilter{})
	for _, event := range events {
		if event.Action == "command.reconciled" {
			t.Fatal("late poll emitted contradictory audit")
		}
	}
	if entry.Outcome != "pending" {
		t.Fatal("Record exposed a mutable internal entry")
	}
}

func TestCommandResolutionPinsUnacknowledgedEntry(t *testing.T) {
	l := NewCommandLog()
	l.Record(&models.CommandLogEntry{RequestID: "old", Outcome: "unknown", SubmittedAt: time.Unix(1, 0)})
	if changed, status := l.resolveUnknown("old", "success", "observed", "actor", ""); !changed || status != 200 {
		t.Fatal("resolution failed")
	}
	l.mu.Lock()
	for i := 0; i < commandLogSliceCap; i++ {
		id := fmt.Sprintf("entry-%d", i)
		l.entries[id] = &models.CommandLogEntry{RequestID: id, Outcome: "success", SubmittedAt: time.Now()}
	}
	l.enforceCap()
	_, exists := l.entries["old"]
	delete(l.resolving, "old")
	l.mu.Unlock()
	if !exists {
		t.Fatal("unacknowledged resolution evicted")
	}
	if changed, status := l.resolveUnknown("old", "success", "observed", "actor", ""); changed || status != 200 {
		t.Fatal("identical retry failed")
	}
	l.mu.Lock()
	pinned := l.resolving["old"]
	l.mu.Unlock()
	if !pinned {
		t.Fatal("retry did not pin entry during persistence")
	}
}

func TestCommandResolutionUnavailablePersistence(t *testing.T) {
	s := newTestStore(t)
	u := resolutionActor(s)
	seedResolution(s)
	s.saveWG.Wait()
	s.backend = nil
	if w := resolutionRequest(s, u, resolutionBody); w.Code != 503 {
		t.Fatalf("unavailable: %d", w.Code)
	}
	if entry := s.commandLog.List(1)[0]; entry.Resolution != nil || entry.Outcome != "unknown" {
		t.Fatal("unavailable persistence changed command")
	}
}

func TestCommandResolutionActorAndTokenRetry(t *testing.T) {
	s := newTestStore(t)
	u := resolutionActor(s)
	seedResolution(s)
	call := func(actor *models.User, token string, scopes []string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/command-log/resolve", strings.NewReader(resolutionBody))
		ctx := context.WithValue(r.Context(), userCtxKey, actor)
		ctx = context.WithValue(ctx, tokenIDCtxKey, token)
		ctx = context.WithValue(ctx, tokenScopesCtxKey, scopes)
		w := httptest.NewRecorder()
		commandResolutionRoute(s).ServeHTTP(w, r.WithContext(ctx))
		return w.Code
	}
	if code := call(u, "token-one", []string{"vm.view"}); code != 403 {
		t.Fatalf("scope: %d", code)
	}
	if code := call(u, "token-one", []string{"user.manage"}); code != 200 {
		t.Fatalf("resolve: %d", code)
	}
	if code := call(u, "token-two", []string{"user.manage"}); code != 409 {
		t.Fatalf("different token: %d", code)
	}
	other := *u
	other.ID = "other"
	if code := call(&other, "token-one", []string{"user.manage"}); code != 409 {
		t.Fatalf("different actor: %d", code)
	}
	if code := call(u, "token-one", []string{"user.manage"}); code != 200 {
		t.Fatalf("identical token retry: %d", code)
	}
}
