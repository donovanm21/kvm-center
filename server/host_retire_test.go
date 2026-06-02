package main

// Tests for the v5.54.0 host-retire deny-list. The retire flow has
// two halves — calling the agent (covered manually) and the
// deny-list guard that prevents telemetry from re-creating the row.
// These tests pin the guard semantics.

import (
	"testing"
)

func TestStore_RetireAndUnretire(t *testing.T) {
	s := newTestStore(t)
	if s.IsHostRemoved("host99.example.com") {
		t.Fatal("fresh store should not have any removed hosts")
	}
	s.MarkHostRemoved("host99.example.com", "u1", "decom")
	if !s.IsHostRemoved("host99.example.com") {
		t.Error("MarkHostRemoved did not mark the host as removed")
	}
	// Short-form should also match the FQDN-keyed entry.
	if !s.IsHostRemoved("host99") {
		t.Error("short-form lookup didn't match FQDN deny-list entry")
	}
	if !s.UnmarkHostRemoved("host99.example.com") {
		t.Error("UnmarkHostRemoved returned false for an existing entry")
	}
	if s.IsHostRemoved("host99.example.com") {
		t.Error("entry still present after UnmarkHostRemoved")
	}
}

func TestStore_RetireShortFormMatchesFQDNEntry(t *testing.T) {
	// Retired by short-form; FQDN telemetry should also be rejected.
	s := newTestStore(t)
	s.MarkHostRemoved("host99", "u1", "")
	if !s.IsHostRemoved("host99.example.com") {
		t.Error("FQDN telemetry should be rejected when short-form is in deny-list")
	}
	if !s.UnmarkHostRemoved("host99.example.com") {
		t.Error("UnmarkHostRemoved by FQDN should clear a short-form entry")
	}
	if s.IsHostRemoved("host99") {
		t.Error("short-form lookup should be clear after FQDN unretire")
	}
}

func TestStore_ListRemovedHosts_SnapshotIsDefensiveCopy(t *testing.T) {
	s := newTestStore(t)
	s.MarkHostRemoved("host99", "u1", "decom")
	snap := s.ListRemovedHosts()
	if len(snap) != 1 {
		t.Fatalf("expected 1 removed host, got %d", len(snap))
	}
	// Mutating the snapshot must not affect the store.
	snap[0].Reason = "tampered"
	again := s.ListRemovedHosts()
	if again[0].Reason != "decom" {
		t.Errorf("snapshot mutation leaked into store: got %q", again[0].Reason)
	}
}
