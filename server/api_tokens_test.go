package main

import (
	"strings"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// API-token unit tests (v5.12.0). Tokens bypass the session-cookie
// auth path entirely, so a miss in the hash comparison or
// revocation gate is a privilege escalation. Tests cover:
//
//   - Secret is never persisted in plaintext.
//   - Lookup accepts the correct token, rejects wrong secrets.
//   - Revocation disables a token immediately.
//   - Expiry is enforced.
//   - User-kind vs system-kind are both honored; both require the
//     owner to be enabled.

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(t.TempDir() + "/data.json")
	// Drain fire-and-forget AsyncSave goroutines before t.TempDir
	// cleanup — otherwise the rename into a removed dir trips
	// RemoveAll with "directory not empty".
	t.Cleanup(func() { s.saveWG.Wait() })
	// ensureBuiltInRoles already ran in NewStore. Seed a user.
	s.mu.Lock()
	s.users["u1"] = &models.User{ID: "u1", Username: "alice", Enabled: true}
	s.mu.Unlock()
	return s
}

func TestCreateAPIToken_HashOnly(t *testing.T) {
	s := newTestStore(t)
	full, rec, err := s.CreateAPIToken("ci", "u1", "user", time.Time{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(full, "kvmc_") {
		t.Errorf("bad token format: %s", full)
	}
	if rec.Hash == "" {
		t.Error("hash empty — persist layer can't verify later")
	}
	// Record stored in the store shouldn't leak the secret.
	stored := s.GetAPIToken(rec.ID)
	if stored == nil {
		t.Fatal("GetAPIToken returned nil")
	}
	if stored.Hash != "" {
		t.Error("GetAPIToken leaks hash to API consumers")
	}
}

func TestLookupAPIToken_RejectsWrongSecret(t *testing.T) {
	s := newTestStore(t)
	full, _, _ := s.CreateAPIToken("ci", "u1", "user", time.Time{}, nil)
	// Flip the last character of the secret.
	bad := full[:len(full)-1] + "0"
	if bad == full {
		bad = full[:len(full)-1] + "1"
	}
	if u := s.LookupAPIToken(bad); u != nil {
		t.Error("corrupted secret still resolved to a user")
	}
	if u := s.LookupAPIToken(full); u == nil || u.ID != "u1" {
		t.Errorf("valid token didn't resolve, got %+v", u)
	}
}

func TestRevokeAPIToken_Immediate(t *testing.T) {
	s := newTestStore(t)
	full, rec, _ := s.CreateAPIToken("ci", "u1", "system", time.Time{}, nil)
	if u := s.LookupAPIToken(full); u == nil {
		t.Fatal("valid token didn't resolve pre-revoke")
	}
	if err := s.RevokeAPIToken(rec.ID); err != nil {
		t.Fatal(err)
	}
	if u := s.LookupAPIToken(full); u != nil {
		t.Error("revoked token still resolved")
	}
}

func TestLookupAPIToken_Expired(t *testing.T) {
	s := newTestStore(t)
	// 1 ms TTL — elapsed by the time we look up.
	full, _, _ := s.CreateAPIToken("ci", "u1", "user", time.Now().Add(-time.Hour), nil)
	if u := s.LookupAPIToken(full); u != nil {
		t.Error("expired token still resolved")
	}
}

func TestLookupAPIToken_DisabledOwner(t *testing.T) {
	s := newTestStore(t)
	full, _, _ := s.CreateAPIToken("ci", "u1", "user", time.Time{}, nil)
	s.mu.Lock()
	s.users["u1"].Enabled = false
	s.mu.Unlock()
	if u := s.LookupAPIToken(full); u != nil {
		t.Error("disabled-owner token still resolved")
	}
}
