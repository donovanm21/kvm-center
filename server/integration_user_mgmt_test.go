package main

// Coverage for the user management endpoints — Settings → Users in
// the SPA. These exercise the "every UI mutation has tests" guarantee
// we tightened in v5.47.

import (
	"net/http"
	"testing"

	"kvm-center/internal/models"
)

// TestIntegration_UserManagement_CreateAndList: an admin can create a
// new user; the new user appears in /api/users with sanitised fields.
func TestIntegration_UserManagement_CreateAndList(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("admin", "admin-password-12")

	// Create a viewer.
	st, body := h.do("POST", "/api/users/new", map[string]any{
		"username": "alice",
		"email":    "alice@example.com",
		"password": "alice-password-12",
		"enabled":  true,
		"role_ids": []string{"role_viewer"},
	})
	if st != http.StatusOK {
		t.Fatalf("create user: status=%d body=%s", st, body)
	}

	var users []*models.User
	if h.doJSON("GET", "/api/users", nil, &users) != http.StatusOK {
		t.Fatal("list users")
	}
	var alice *models.User
	for _, u := range users {
		if u.Username == "alice" {
			alice = u
		}
	}
	if alice == nil {
		t.Fatal("alice not in /api/users")
	}
	if alice.PasswordHash != "" {
		t.Fatalf("/api/users leaked password hash for %q", alice.Username)
	}
	if !alice.Enabled || len(alice.RoleIDs) != 1 || alice.RoleIDs[0] != "role_viewer" {
		t.Fatalf("alice fields wrong: %+v", alice)
	}
}

// TestIntegration_UserManagement_LastAdminGuard: cannot delete or
// demote the last enabled admin; cannot disable them either.
func TestIntegration_UserManagement_LastAdminGuard(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("admin", "admin-password-12")

	// Find admin's ID via the list.
	var users []*models.User
	h.doJSON("GET", "/api/users", nil, &users)
	var adminID string
	for _, u := range users {
		if u.Username == "admin" {
			adminID = u.ID
		}
	}
	if adminID == "" {
		t.Fatal("admin user not found")
	}

	// Try to delete — should refuse.
	st, _ := h.do("DELETE", "/api/users/"+adminID, nil)
	if st != http.StatusForbidden {
		t.Fatalf("DELETE last admin: want 403, got %d", st)
	}

	// Try to disable — should refuse.
	disabled := false
	st, _ = h.do("PUT", "/api/users/"+adminID, map[string]any{"enabled": &disabled})
	if st != http.StatusBadRequest {
		t.Fatalf("disable last admin: want 400, got %d", st)
	}

	// Try to remove admin role — should refuse.
	st, _ = h.do("PUT", "/api/users/"+adminID, map[string]any{
		"role_ids": []string{"role_viewer"},
	})
	if st != http.StatusBadRequest {
		t.Fatalf("demote last admin: want 400, got %d", st)
	}
}

// TestIntegration_UserManagement_NonAdminRefused: a viewer cannot
// list or create users.
func TestIntegration_UserManagement_NonAdminRefused(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("admin", "admin-password-12")
	_, _ = h.do("POST", "/api/auth/logout", nil)
	h.seedUser("viewer", "viewer-password-1", []string{"role_viewer"})
	h.mustLogin("viewer", "viewer-password-1")

	if st, _ := h.do("GET", "/api/users", nil); st != http.StatusForbidden {
		t.Fatalf("viewer GET /api/users: want 403, got %d", st)
	}
	if st, _ := h.do("POST", "/api/users/new", map[string]any{
		"username": "bob", "password": "bob-password-12", "role_ids": []string{"role_viewer"},
	}); st != http.StatusForbidden {
		t.Fatalf("viewer POST /api/users/new: want 403, got %d", st)
	}
}

// TestIntegration_UserManagement_UpdateEmail: PUT updates a single
// field without touching others.
func TestIntegration_UserManagement_UpdateEmail(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("admin", "admin-password-12")
	bob := h.seedUser("bob", "bob-password-12", []string{"role_viewer"})

	newEmail := "bob.new@example.com"
	st, body := h.do("PUT", "/api/users/"+bob.ID, map[string]any{"email": &newEmail})
	if st != http.StatusOK {
		t.Fatalf("update email: status=%d body=%s", st, body)
	}

	updated := h.Store.GetUserByID(bob.ID)
	if updated == nil || updated.Email != newEmail {
		t.Fatalf("email not updated: %+v", updated)
	}
	// Other fields preserved.
	if !updated.Enabled || len(updated.RoleIDs) != 1 {
		t.Fatalf("update leaked into other fields: %+v", updated)
	}
}
