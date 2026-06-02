package agentauth

import (
	"testing"
	"time"
)

const testSecret = "f00dcafe-deadbeef-1234567890abcdef"

func TestSignVerifyRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	auth, exp, nonce, err := Sign(testSecret, "host-a", "POST", "/command", now, 0)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := Verify(testSecret, "host-a", "POST", "/command", auth, exp, nonce, now); err != nil {
		t.Fatalf("Verify of a fresh signature should pass, got %v", err)
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	auth, exp, nonce, _ := Sign(testSecret, "host-a", "POST", "/command", now, 0)

	cases := map[string]func() error{
		"wrong secret": func() error {
			return Verify("other-secret", "host-a", "POST", "/command", auth, exp, nonce, now)
		},
		"wrong host": func() error {
			return Verify(testSecret, "host-b", "POST", "/command", auth, exp, nonce, now)
		},
		"wrong method": func() error {
			return Verify(testSecret, "host-a", "GET", "/command", auth, exp, nonce, now)
		},
		"wrong path": func() error {
			return Verify(testSecret, "host-a", "POST", "/vm-create", auth, exp, nonce, now)
		},
		"tampered auth": func() error {
			return Verify(testSecret, "host-a", "POST", "/command", auth+"x", exp, nonce, now)
		},
		"tampered nonce": func() error {
			return Verify(testSecret, "host-a", "POST", "/command", auth, exp, nonce+"x", now)
		},
		"missing auth": func() error {
			return Verify(testSecret, "host-a", "POST", "/command", "", exp, nonce, now)
		},
	}
	for name, fn := range cases {
		if err := fn(); err == nil {
			t.Errorf("%s: expected verification failure, got nil", name)
		}
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	signedAt := time.Unix(1_700_000_000, 0)
	auth, exp, nonce, _ := Sign(testSecret, "host-a", "POST", "/command", signedAt, 30*time.Second)

	// Still valid inside the window.
	if err := Verify(testSecret, "host-a", "POST", "/command", auth, exp, nonce, signedAt.Add(29*time.Second)); err != nil {
		t.Fatalf("within window should pass, got %v", err)
	}
	// Expired past the window.
	if err := Verify(testSecret, "host-a", "POST", "/command", auth, exp, nonce, signedAt.Add(31*time.Second)); err == nil {
		t.Fatal("past expiry should fail, got nil")
	}
}

func TestMethodCaseInsensitive(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	auth, exp, nonce, _ := Sign(testSecret, "host-a", "post", "/command", now, 0)
	if err := Verify(testSecret, "host-a", "POST", "/command", auth, exp, nonce, now); err != nil {
		t.Fatalf("method comparison should be case-insensitive, got %v", err)
	}
}

func TestSignRequiresSecret(t *testing.T) {
	if _, _, _, err := Sign("", "host-a", "POST", "/command", time.Now(), 0); err == nil {
		t.Fatal("Sign with empty secret should error")
	}
	if err := Verify("", "host-a", "POST", "/command", "x", "1", "n", time.Now()); err == nil {
		t.Fatal("Verify with empty secret should error")
	}
}
