package main

// Authentication integration flows (v5.27). Exercises the full
// register/login/me/logout loop through the middleware stack to catch
// wiring regressions the per-handler tests miss.

import (
	"net/http"
	"testing"
)

// TestIntegration_RegisterFirstAdmin_Login_Me covers the happy path
// a fresh operator walks: /api/auth/register mints the first user
// and auto-elevates them to admin, /login sets a session cookie,
// /me returns the admin's profile, /logout clears it.
func TestIntegration_RegisterFirstAdmin_Login_Me(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "correct-horse-battery-staple")

	// /me before explicit login — registration already sessions us in.
	// Response shape: {"user": {...}, "permissions": [...]}.
	var me struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
		Permissions []string `json:"permissions"`
	}
	if st := h.doJSON("GET", "/api/auth/me", nil, &me); st != http.StatusOK {
		t.Fatalf("/api/auth/me post-register: status=%d", st)
	}
	if me.User.Username != "alice" {
		t.Fatalf("/me username: want alice, got %q", me.User.Username)
	}
	if len(me.Permissions) == 0 {
		t.Fatalf("/me perms empty for first admin")
	}

	// Logout drops the cookie; /me then refuses.
	if st, _ := h.do("POST", "/api/auth/logout", nil); st != http.StatusOK {
		t.Fatalf("logout: status=%d", st)
	}
	st, _ := h.do("GET", "/api/auth/me", nil)
	if st != http.StatusUnauthorized {
		t.Fatalf("/me after logout: want 401, got %d", st)
	}

	// Re-login with the saved password works.
	h.mustLogin("alice", "correct-horse-battery-staple")
	if st := h.doJSON("GET", "/api/auth/me", nil, &me); st != http.StatusOK {
		t.Fatalf("/me after re-login: status=%d", st)
	}
}

// TestIntegration_Login_BadPassword: negative path — wrong password
// returns 401 and does not set a session cookie.
func TestIntegration_Login_BadPassword(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "right-password")
	// Clear cookies so we're acting as a hostile fresh client.
	_, _ = h.do("POST", "/api/auth/logout", nil)

	if st := h.login("alice", "wrong-password"); st != http.StatusUnauthorized {
		t.Fatalf("bad login: want 401, got %d", st)
	}
	st, _ := h.do("GET", "/api/auth/me", nil)
	if st != http.StatusUnauthorized {
		t.Fatalf("/me after bad login: want 401, got %d", st)
	}
}

// TestIntegration_Register_ClosedAfterFirstUser: /api/auth/register is
// bootstrap-only. Once at least one user exists it refuses; admins
// must use POST /api/users/new instead (which the Operator role
// doesn't get). Protects against a fresh kvm-center being re-seeded
// by whoever hits /register first.
func TestIntegration_Register_ClosedAfterFirstUser(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")
	_, _ = h.do("POST", "/api/auth/logout", nil)

	st, body := h.do("POST", "/api/auth/register", map[string]string{
		"username": "bob",
		"password": "bob-password-34",
	})
	if st != http.StatusForbidden {
		t.Fatalf("second register: want 403, got %d (body=%s)", st, body)
	}
}

// TestIntegration_NonAdmin_Cannot_ListUsers: a seeded Viewer user
// signs in and is refused /api/users (needs user.manage).
func TestIntegration_NonAdmin_Cannot_ListUsers(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")
	_, _ = h.do("POST", "/api/auth/logout", nil)

	h.seedUser("bob", "bob-password-34", []string{"role_viewer"})
	h.mustLogin("bob", "bob-password-34")

	st, _ := h.do("GET", "/api/users", nil)
	if st != http.StatusForbidden {
		t.Fatalf("viewer /api/users: want 403, got %d", st)
	}
}
