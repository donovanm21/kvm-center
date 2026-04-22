package main

import (
	"strings"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// TestCommandLog_RecordReplay: second Record with same RequestID
// returns the existing entry + replay=true.
func TestCommandLog_RecordReplay(t *testing.T) {
	l := NewCommandLog()
	id := "req_abc"
	e1 := &models.CommandLogEntry{RequestID: id, Action: "START", Outcome: "pending", SubmittedAt: time.Now()}
	got, replay := l.Record(e1)
	if replay {
		t.Fatal("first Record should not be a replay")
	}
	if got.RequestID != id {
		t.Fatalf("Record returned wrong id: %s", got.RequestID)
	}
	e2 := &models.CommandLogEntry{RequestID: id, Action: "STOP"}
	got, replay = l.Record(e2)
	if !replay {
		t.Fatal("second Record should be a replay")
	}
	if got.Action != "START" {
		t.Fatalf("replay returned mutated entry: action=%s", got.Action)
	}
}

// TestCommandLog_UpdateOutcome: Update stamps outcome + expires_at
// for terminal states.
func TestCommandLog_UpdateOutcome(t *testing.T) {
	l := NewCommandLog()
	id := "req_x"
	l.Record(&models.CommandLogEntry{RequestID: id, Outcome: "pending", SubmittedAt: time.Now()})
	l.Update(id, "success", "ok", 200, false)
	list := l.List(0)
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	e := list[0]
	if e.Outcome != "success" || e.StatusCode != 200 {
		t.Fatalf("entry not updated: %+v", e)
	}
	if e.ExpiresAt == nil {
		t.Fatal("terminal entry should have ExpiresAt set")
	}
	if e.AppliedAt == nil {
		t.Fatal("terminal entry should have AppliedAt set")
	}
}

// TestCommandLog_PendingAge: Pending returns only entries older than
// the cutoff.
func TestCommandLog_PendingAge(t *testing.T) {
	l := NewCommandLog()
	now := time.Now()
	l.Record(&models.CommandLogEntry{RequestID: "fresh", Outcome: "pending", SubmittedAt: now})
	l.Record(&models.CommandLogEntry{RequestID: "old", Outcome: "pending", SubmittedAt: now.Add(-time.Hour)})
	l.Record(&models.CommandLogEntry{RequestID: "old-done", Outcome: "success", SubmittedAt: now.Add(-time.Hour)})

	pending := l.Pending(30 * time.Minute)
	if len(pending) != 1 {
		t.Fatalf("Pending returned %d entries; want 1", len(pending))
	}
	if pending[0].RequestID != "old" {
		t.Fatalf("expected 'old' pending, got %s", pending[0].RequestID)
	}
}

// TestCommandLog_EnforceCap: when cap is exceeded, oldest TERMINAL
// entries drop; pending always kept.
func TestCommandLog_EnforceCap(t *testing.T) {
	l := NewCommandLog()
	// Insert > cap terminal + one pending. Reuse the internal
	// replace() path so the test isn't constrained by
	// commandLogSliceCap from production.
	m := make(map[string]*models.CommandLogEntry)
	base := time.Now().Add(-48 * time.Hour)
	for i := 0; i < commandLogSliceCap+5; i++ {
		id := "req_t_" + itoaShort(i)
		m[id] = &models.CommandLogEntry{
			RequestID:   id,
			Outcome:     "success",
			SubmittedAt: base.Add(time.Duration(i) * time.Second),
		}
	}
	m["req_pending"] = &models.CommandLogEntry{
		RequestID:   "req_pending",
		Outcome:     "pending",
		SubmittedAt: base,
	}
	l.replace(m)
	list := l.List(0)
	if len(list) > commandLogSliceCap {
		t.Fatalf("after cap: %d entries, want <= %d", len(list), commandLogSliceCap)
	}
	found := false
	for _, e := range list {
		if e.RequestID == "req_pending" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("pending entry was evicted; cap must never drop non-terminal entries")
	}
}

// TestNewRequestID_Unique: trivial — two consecutive generations
// shouldn't collide.
func TestNewRequestID_Unique(t *testing.T) {
	a := NewRequestID()
	b := NewRequestID()
	if a == b {
		t.Fatal("two random IDs collided")
	}
	if !strings.HasPrefix(a, "req_") {
		t.Fatalf("unexpected prefix: %s", a)
	}
}

func itoaShort(i int) string {
	if i == 0 {
		return "0"
	}
	b := make([]byte, 0, 6)
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
