package main

import (
	"testing"
	"time"

	"kvm-center/internal/models"
)

func TestObjectLock_RefusesConflictByUUID(t *testing.T) {
	l := NewCommandLog()
	now := time.Now()
	l.Record(&models.CommandLogEntry{RequestID: "first", Action: "MIGRATE", VMUUID: "uuid-1", Outcome: "pending", SubmittedAt: now})

	got := findInflightConflict(l, "STOP", "uuid-1", "ignored-name", "second")
	if got == nil {
		t.Fatal("expected conflict for same UUID")
	}
	if got.RequestID != "first" {
		t.Fatalf("conflicting id = %s", got.RequestID)
	}
}

func TestObjectLock_RefusesConflictByName(t *testing.T) {
	l := NewCommandLog()
	l.Record(&models.CommandLogEntry{RequestID: "first", Action: "MIGRATE", VMName: "web01", Outcome: "pending", SubmittedAt: time.Now()})

	if got := findInflightConflict(l, "STOP", "", "web01", "second"); got == nil {
		t.Fatal("expected conflict by name when uuid empty")
	}
}

func TestObjectLock_AllowsDifferentVM(t *testing.T) {
	l := NewCommandLog()
	l.Record(&models.CommandLogEntry{RequestID: "first", VMUUID: "uuid-1", Outcome: "pending", SubmittedAt: time.Now()})

	if got := findInflightConflict(l, "MIGRATE", "uuid-2", "", "second"); got != nil {
		t.Fatalf("unexpected conflict against different VM: %+v", got)
	}
}

func TestObjectLock_AllowsTerminalEntries(t *testing.T) {
	l := NewCommandLog()
	l.Record(&models.CommandLogEntry{RequestID: "done", VMUUID: "uuid-1", Outcome: "pending", SubmittedAt: time.Now()})
	l.Update("done", "success", "ok", 200, false)

	if got := findInflightConflict(l, "MIGRATE", "uuid-1", "", "next"); got != nil {
		t.Fatalf("terminal entry should not conflict: %+v", got)
	}
}

func TestObjectLock_OwnRequestIDExcluded(t *testing.T) {
	l := NewCommandLog()
	l.Record(&models.CommandLogEntry{RequestID: "self", VMUUID: "uuid-1", Outcome: "pending", SubmittedAt: time.Now()})

	if got := findInflightConflict(l, "MIGRATE", "uuid-1", "", "self"); got != nil {
		t.Fatalf("own request_id should not self-conflict: %+v", got)
	}
}

func TestObjectLock_FleetWideEmptyTargetSkipped(t *testing.T) {
	l := NewCommandLog()
	l.Record(&models.CommandLogEntry{RequestID: "first", Outcome: "pending", SubmittedAt: time.Now()})
	if got := findInflightConflict(l, "REFRESH", "", "", "second"); got != nil {
		t.Fatalf("empty target shouldn't gate: %+v", got)
	}
}
