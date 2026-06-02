package main

import (
	"testing"
	"time"

	"kvm-center/internal/models"
)

// TestSave_PreservesBsonOnlyFields exercises the v5.60.3 fix: fields
// tagged `json:"-" bson:"…"` must survive the deep-copy round-trip
// inside Save. Pre-fix, the JSON marshal/unmarshal stripped them and
// the backend stored an empty value (so Mongo would write
// `discord_gateway.token=""` on every Save → operator had to Re-pair
// after every restart).
func TestSave_PreservesBsonOnlyFields(t *testing.T) {
	stub := &stubBackend{name: "stub"}
	s := NewStoreWithBackend(stub, t.TempDir()+"/unused.json")
	// Drain the boot-time AsyncSave goroutines from ensureBuiltInRoles
	// / ensureSecurityDefaults before mutating, so the synchronous
	// Save below is the only thing stub.stored reflects.
	s.saveWG.Wait()
	t.Cleanup(func() { s.saveWG.Wait() })

	now := time.Now().UTC()
	expiry := now.Add(24 * time.Hour)

	s.mu.Lock()
	s.discordGateway = &models.DiscordGateway{
		Enabled:        true,
		Host:           "gw.local",
		Port:           8443,
		TLS:            true,
		RegistrationID: "reg-abc",
		WebhookID:      "kvm-center",
		Token:          "bearer-secret-token",
		Status:         "healthy",
	}
	s.pendingEnrollments = map[string]*models.PendingEnrollment{
		"pe-1": {
			ID:            "pe-1",
			PollTokenHash: "hash-pe-1",
			IssuedCertPEM: "-----CERT-----",
			IssuedCAPEM:   "-----CA-----",
		},
	}
	// CommandLog uses an inner mutex; populate via Record so the
	// snapshot returns the entry we expect.
	s.mu.Unlock()
	s.commandLog.Record(&models.CommandLogEntry{
		RequestID:   "cmd-1",
		Action:      "REBOOT_VM",
		SubmittedAt: now,
		ExpiresAt:   &expiry,
		Outcome:     "pending",
	})

	s.Save()

	if stub.stored == nil {
		t.Fatal("stubBackend.stored is nil — Save didn't reach backend")
	}
	if stub.stored.DiscordGateway == nil {
		t.Fatal("DiscordGateway not stored")
	}
	if got := stub.stored.DiscordGateway.Token; got != "bearer-secret-token" {
		t.Errorf("DiscordGateway.Token: got %q, want %q (json:\"-\" strip not re-attached)", got, "bearer-secret-token")
	}
	pe, ok := stub.stored.PendingEnrollments["pe-1"]
	if !ok || pe == nil {
		t.Fatal("PendingEnrollment pe-1 not stored")
	}
	if pe.PollTokenHash != "hash-pe-1" {
		t.Errorf("PendingEnrollment.PollTokenHash: got %q, want %q", pe.PollTokenHash, "hash-pe-1")
	}
	if pe.IssuedCertPEM != "-----CERT-----" {
		t.Errorf("PendingEnrollment.IssuedCertPEM lost: got %q", pe.IssuedCertPEM)
	}
	if pe.IssuedCAPEM != "-----CA-----" {
		t.Errorf("PendingEnrollment.IssuedCAPEM lost: got %q", pe.IssuedCAPEM)
	}
	cmd, ok := stub.stored.CommandLog["cmd-1"]
	if !ok || cmd == nil {
		t.Fatal("CommandLog cmd-1 not stored")
	}
	if cmd.ExpiresAt == nil || !cmd.ExpiresAt.Equal(expiry) {
		t.Errorf("CommandLogEntry.ExpiresAt: got %v, want %v", cmd.ExpiresAt, expiry)
	}
}
