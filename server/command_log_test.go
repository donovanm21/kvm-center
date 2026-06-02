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

// TestMarkPendingOnTimeout (v5.47.2): an in-flight entry whose HTTP
// call to the agent times out is kept "pending" with InflightUntil
// set, not flipped to "failed". The boot reconciler then leaves it
// alone until the deadline passes, at which point Pending() returns
// it for resolution.
func TestMarkPendingOnTimeout(t *testing.T) {
	l := NewCommandLog()
	id := "req_timeout"
	l.Record(&models.CommandLogEntry{RequestID: id, Outcome: "pending", SubmittedAt: time.Now()})

	deadline := time.Now().Add(outcomeReconcileWindow)
	l.MarkPending(id, deadline, "agent call timed out: deadline exceeded")

	list := l.List(0)
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	e := list[0]
	if e.Outcome != "pending" {
		t.Fatalf("outcome = %q, want pending (transport error must not lie about failure)", e.Outcome)
	}
	if e.InflightUntil.IsZero() {
		t.Fatal("InflightUntil should be set on a timed-out entry")
	}
	if !strings.Contains(e.Message, "timed out") {
		t.Fatalf("message should describe timeout, got %q", e.Message)
	}
	if e.AppliedAt != nil {
		t.Fatal("AppliedAt should not be set on still-pending entry")
	}
	if e.ExpiresAt != nil {
		t.Fatal("ExpiresAt should not be set on still-pending entry")
	}

	// Boot reconciler with the legacy 45-min cutoff should NOT pick
	// this up — the live reconciler owns it until the deadline passes.
	if got := l.Pending(pendingReconcileAge); len(got) != 0 {
		t.Fatalf("Pending() returned %d entries before deadline; want 0", len(got))
	}

	// Once the deadline passes, the entry surfaces for resolution.
	l.mu.Lock()
	l.entries[id].InflightUntil = time.Now().Add(-time.Second)
	l.entries[id].SubmittedAt = time.Now().Add(-time.Hour)
	l.mu.Unlock()
	if got := l.Pending(pendingReconcileAge); len(got) != 1 {
		t.Fatalf("Pending() after deadline returned %d entries; want 1", len(got))
	}
}

// TestMarkPending_NoOpAfterTerminal: if the agent races the
// reconciler and reports a terminal state first, MarkPending must
// not regress the entry back to pending.
func TestMarkPending_NoOpAfterTerminal(t *testing.T) {
	l := NewCommandLog()
	id := "req_done"
	l.Record(&models.CommandLogEntry{RequestID: id, Outcome: "pending", SubmittedAt: time.Now()})
	l.Update(id, "success", "ok", 200, false)
	l.MarkPending(id, time.Now().Add(time.Hour), "should be ignored")

	list := l.List(0)
	if list[0].Outcome != "success" {
		t.Fatalf("MarkPending regressed terminal entry: outcome=%s", list[0].Outcome)
	}
	if !list[0].InflightUntil.IsZero() {
		t.Fatal("InflightUntil should not be set on a terminal entry")
	}
}

// TestRecordPoll bumps LastPolledAt without touching anything else.
func TestRecordPoll(t *testing.T) {
	l := NewCommandLog()
	id := "req_p"
	l.Record(&models.CommandLogEntry{RequestID: id, Outcome: "pending", SubmittedAt: time.Now()})
	l.MarkPending(id, time.Now().Add(time.Hour), "polling")
	at := time.Now()
	l.RecordPoll(id, at)
	got := l.List(0)[0]
	if !got.LastPolledAt.Equal(at) {
		t.Fatalf("LastPolledAt = %v, want %v", got.LastPolledAt, at)
	}
	if got.Outcome != "pending" {
		t.Fatalf("RecordPoll changed outcome to %s", got.Outcome)
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
