package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kvm-center/internal/agentauth"
)

// resetCmdAuthState clears the shared console/command-auth package state so
// each subtest starts clean.
func resetCmdAuthState() {
	consoleSecMu.Lock()
	consoleSecret = ""
	consoleSecretPrev = ""
	consoleSecretRot = time.Time{}
	hostIDForToken = "test-host"
	consoleSecMu.Unlock()
	commandAuthActive.Store(false)
}

// signedReq builds a request to path carrying a valid command-auth signature
// for the given secret + host.
func signedReq(t *testing.T, secret, host, method, path string) *http.Request {
	t.Helper()
	auth, exp, nonce, err := agentauth.Sign(secret, host, method, path, time.Now(), 0)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set(agentauth.HeaderAuth, auth)
	r.Header.Set(agentauth.HeaderExp, exp)
	r.Header.Set(agentauth.HeaderNonce, nonce)
	return r
}

func serve(h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, r)
	return rec
}

func okHandler(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

func TestCommandAuth_InactivePassesThrough(t *testing.T) {
	resetCmdAuthState()
	// No secret, not active, no headers: back-compat accept.
	r := httptest.NewRequest("POST", "/command", nil)
	rec := serve(requireCommandAuth(okHandler), r)
	if rec.Code != http.StatusOK {
		t.Fatalf("inactive should pass through, got %d", rec.Code)
	}
}

func TestCommandAuth_ActiveValidSignature(t *testing.T) {
	resetCmdAuthState()
	const secret = "shared-fleet-secret"
	SetConsoleSecret(secret)
	commandAuthActive.Store(true)

	rec := serve(requireCommandAuth(okHandler), signedReq(t, secret, "test-host", "POST", "/command"))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid signature should pass, got %d", rec.Code)
	}
}

func TestCommandAuth_ActiveRejectsBad(t *testing.T) {
	resetCmdAuthState()
	const secret = "shared-fleet-secret"
	SetConsoleSecret(secret)
	commandAuthActive.Store(true)

	// Missing headers entirely.
	if rec := serve(requireCommandAuth(okHandler), httptest.NewRequest("POST", "/command", nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("missing signature: want 401, got %d", rec.Code)
	}
	// Wrong secret.
	if rec := serve(requireCommandAuth(okHandler), signedReq(t, "wrong-secret", "test-host", "POST", "/command")); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret: want 401, got %d", rec.Code)
	}
	// Wrong host binding.
	if rec := serve(requireCommandAuth(okHandler), signedReq(t, secret, "other-host", "POST", "/command")); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong host: want 401, got %d", rec.Code)
	}
	// Cross-path replay: sign for /vm-create, deliver the headers to /command.
	replay := signedReq(t, secret, "test-host", "POST", "/vm-create")
	onCommand := httptest.NewRequest("POST", "/command", nil)
	onCommand.Header = replay.Header
	if rec := serve(requireCommandAuth(okHandler), onCommand); rec.Code != http.StatusUnauthorized {
		t.Errorf("cross-path replay: want 401, got %d", rec.Code)
	}
}

func TestCommandAuth_ActiveNoSecretFailsClosed(t *testing.T) {
	resetCmdAuthState()
	commandAuthActive.Store(true) // active but secret == ""
	rec := serve(requireCommandAuth(okHandler), httptest.NewRequest("POST", "/command", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("active+no-secret should fail closed with 503, got %d", rec.Code)
	}
}

func TestCommandAuth_PreviousSecretGrace(t *testing.T) {
	resetCmdAuthState()
	SetConsoleSecret("old-secret")
	SetConsoleSecret("new-secret") // rotation: old becomes prev within grace
	commandAuthActive.Store(true)

	// A request signed with the OLD secret still verifies inside the grace.
	rec := serve(requireCommandAuth(okHandler), signedReq(t, "old-secret", "test-host", "POST", "/command"))
	if rec.Code != http.StatusOK {
		t.Fatalf("previous-secret grace should accept, got %d", rec.Code)
	}
}
