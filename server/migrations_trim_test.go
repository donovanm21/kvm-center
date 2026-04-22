package main

import (
	"testing"

	"kvm-center/internal/models"
)

// TestTrimMigrations_NoOpUnderCap: len <= cap → unchanged.
func TestTrimMigrations_NoOpUnderCap(t *testing.T) {
	in := []*models.MigrationRecord{
		{ID: "a", Status: "complete"},
		{ID: "b", Status: "running"},
	}
	out := trimMigrations(in, 10)
	if len(out) != 2 {
		t.Fatalf("unexpected len %d", len(out))
	}
}

// TestTrimMigrations_DropsOldestTerminal: cap of 2, four entries
// with varied status → terminal entries drop, active always kept.
func TestTrimMigrations_DropsOldestTerminal(t *testing.T) {
	in := []*models.MigrationRecord{
		{ID: "1", Status: "complete"},
		{ID: "2", Status: "failed"},
		{ID: "3", Status: "running"},
		{ID: "4", Status: "complete"},
	}
	out := trimMigrations(in, 2)
	if len(out) != 2 {
		t.Fatalf("len after trim = %d, want 2", len(out))
	}
	// Running record must survive.
	found := false
	for _, m := range out {
		if m.ID == "3" {
			found = true
		}
	}
	if !found {
		t.Fatal("active (running) migration dropped; must always be kept")
	}
}

// TestTrimMigrations_AllActiveStayOverCap: when everything is active
// the slice grows past cap — correctness > memory.
func TestTrimMigrations_AllActiveStayOverCap(t *testing.T) {
	in := []*models.MigrationRecord{
		{ID: "r1", Status: "running"},
		{ID: "r2", Status: "running"},
		{ID: "r3", Status: "running"},
	}
	out := trimMigrations(in, 1)
	if len(out) != 3 {
		t.Fatalf("active-only slice was trimmed to %d; want untouched", len(out))
	}
}
