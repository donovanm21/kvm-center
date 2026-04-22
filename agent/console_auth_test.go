package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

// sign produces a token for verifyConsoleToken with the given secret.
func sign(secret, host, vm string, exp int64, nonce string) string {
	h := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(h, "%s|%s|%d|%s", host, vm, exp, nonce)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// TestConsoleToken_PrevSecretAcceptedDuringGrace: after SetConsoleSecret
// rotates the value, a token minted with the OLD secret still verifies
// as long as we're inside consoleSecretGraceWindow.
func TestConsoleToken_PrevSecretAcceptedDuringGrace(t *testing.T) {
	// Reset package-level state so parallel tests don't leak into us.
	consoleSecMu.Lock()
	consoleSecret, consoleSecretPrev, hostIDForToken = "", "", "hv01"
	consoleSecretRot = time.Time{}
	consoleSecMu.Unlock()

	SetConsoleSecret("secret-A")
	SetConsoleSecret("secret-B") // rotation: prev = secret-A

	exp := time.Now().Add(2 * time.Minute).Unix()
	nonce := "n1"
	oldToken := sign("secret-A", "hv01", "vm1", exp, nonce)
	newToken := sign("secret-B", "hv01", "vm1", exp, nonce)

	// Both should verify.
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"current", newToken},
		{"previous", oldToken},
	} {
		r := httptest.NewRequest("GET", fmt.Sprintf("/x?token=%s&exp=%d&nonce=%s", tc.token, exp, nonce), nil)
		if err := verifyConsoleToken(r, "vm1"); err != nil {
			t.Fatalf("%s token rejected: %v", tc.name, err)
		}
	}
}

// TestConsoleToken_PrevSecretRejectedAfterGrace: after the grace
// window elapses, the old secret no longer verifies.
func TestConsoleToken_PrevSecretRejectedAfterGrace(t *testing.T) {
	consoleSecMu.Lock()
	consoleSecret, consoleSecretPrev, hostIDForToken = "", "", "hv01"
	consoleSecretRot = time.Time{}
	consoleSecMu.Unlock()

	SetConsoleSecret("old")
	SetConsoleSecret("new")

	// Fast-forward the rotation timestamp past the grace window.
	consoleSecMu.Lock()
	consoleSecretRot = time.Now().Add(-2 * consoleSecretGraceWindow)
	consoleSecMu.Unlock()

	exp := time.Now().Add(2 * time.Minute).Unix()
	nonce := "n2"
	tok := sign("old", "hv01", "vm1", exp, nonce)
	r := httptest.NewRequest("GET", fmt.Sprintf("/x?token=%s&exp=%d&nonce=%s", tok, exp, nonce), nil)
	if err := verifyConsoleToken(r, "vm1"); err == nil {
		t.Fatal("expired-grace previous secret was accepted")
	}
}

// TestConsoleToken_UnknownSecretRejected: random third secret, never
// used by the server, always rejected.
func TestConsoleToken_UnknownSecretRejected(t *testing.T) {
	consoleSecMu.Lock()
	consoleSecret, consoleSecretPrev, hostIDForToken = "", "", "hv01"
	consoleSecretRot = time.Time{}
	consoleSecMu.Unlock()

	SetConsoleSecret("real")
	exp := time.Now().Add(2 * time.Minute).Unix()
	nonce := "n3"
	tok := sign("fake", "hv01", "vm1", exp, nonce)
	r := httptest.NewRequest("GET", fmt.Sprintf("/x?token=%s&exp=%d&nonce=%s", tok, exp, nonce), nil)
	if err := verifyConsoleToken(r, "vm1"); err == nil {
		t.Fatal("token signed with unknown secret was accepted")
	}
}
