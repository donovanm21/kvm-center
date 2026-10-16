package main

// Tests for the v5.48 ops.db substrate.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestOpsStore(t *testing.T) (*OperationsStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "operations.db")
	s, err := OpenOperationsStore(path)
	if err != nil {
		t.Fatalf("OpenOperationsStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestOpsStore_InsertAndGet(t *testing.T) {
	s, _ := newTestOpsStore(t)
	ctx := context.Background()
	op := Operation{
		RequestID:   "req_1",
		Action:      "START",
		VMName:      "test-vm",
		VMUUID:      "uuid-1",
		Params:      map[string]string{"vmid": "42"},
		State:       OpStatePending,
		SubmittedAt: time.Now(),
	}
	if err := s.Insert(ctx, op); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := s.Get(ctx, "req_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil after Insert")
	}
	if got.RequestID != "req_1" || got.Action != "START" {
		t.Fatalf("got = %+v", got)
	}
	if got.Params["vmid"] != "42" {
		t.Fatalf("params not roundtripped: %+v", got.Params)
	}
}

func TestOpsStore_InsertIdempotent(t *testing.T) {
	s, _ := newTestOpsStore(t)
	ctx := context.Background()
	op := Operation{RequestID: "req_idem", Action: "STOP", State: OpStatePending, SubmittedAt: time.Now()}
	if err := s.Insert(ctx, op); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	op2 := op
	op2.Action = "MUTATED" // should NOT overwrite
	if err := s.Insert(ctx, op2); err != nil {
		t.Fatalf("second Insert: %v", err)
	}
	got, _ := s.Get(ctx, "req_idem")
	if got.Action != "STOP" {
		t.Fatalf("idempotent insert overwrote existing row: action=%s", got.Action)
	}
}

func TestOpsStore_UpdateStateTransitions(t *testing.T) {
	s, _ := newTestOpsStore(t)
	ctx := context.Background()
	now := time.Now()
	_ = s.Insert(ctx, Operation{RequestID: "req_u", Action: "MIGRATE", State: OpStatePending, SubmittedAt: now})

	started := now.Add(time.Second)
	if err := s.UpdateState(ctx, "req_u", UpdateStateOpts{
		State:     OpStateRunning,
		StartedAt: &started,
		ChildPID:  4321,
	}); err != nil {
		t.Fatalf("UpdateState running: %v", err)
	}
	got, _ := s.Get(ctx, "req_u")
	if got.State != OpStateRunning || got.ChildPID != 4321 || got.StartedAt == nil {
		t.Fatalf("after running: %+v", got)
	}

	completed := now.Add(time.Minute)
	if err := s.UpdateState(ctx, "req_u", UpdateStateOpts{
		State:         OpStateSuccess,
		Output:        "migration complete",
		ClearChildPID: true,
		CompletedAt:   &completed,
	}); err != nil {
		t.Fatalf("UpdateState success: %v", err)
	}
	got, _ = s.Get(ctx, "req_u")
	if got.State != OpStateSuccess || got.ChildPID != 0 || got.CompletedAt == nil {
		t.Fatalf("after success: %+v", got)
	}
	if got.Output != "migration complete" {
		t.Fatalf("output not stored: %q", got.Output)
	}
}

func TestOpsStore_UpdateStateUnknownReturnsErrNoRows(t *testing.T) {
	s, _ := newTestOpsStore(t)
	err := s.UpdateState(context.Background(), "absent", UpdateStateOpts{State: OpStateSuccess})
	if err == nil {
		t.Fatal("expected ErrNoRows, got nil")
	}
}

func TestOpsStore_RecordHeartbeatNoOpOnTerminal(t *testing.T) {
	s, _ := newTestOpsStore(t)
	ctx := context.Background()
	now := time.Now()
	_ = s.Insert(ctx, Operation{RequestID: "req_h", Action: "STOP", State: OpStatePending, SubmittedAt: now})
	completed := now.Add(time.Minute)
	_ = s.UpdateState(ctx, "req_h", UpdateStateOpts{State: OpStateSuccess, CompletedAt: &completed})

	at := now.Add(2 * time.Minute)
	if err := s.RecordHeartbeat(ctx, "req_h", at); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}
	got, _ := s.Get(ctx, "req_h")
	if got.LastHeartbeatAt != nil {
		t.Fatalf("heartbeat written to terminal row: %v", *got.LastHeartbeatAt)
	}
}

func TestOpsStore_RecordHeartbeatStampsRunning(t *testing.T) {
	s, _ := newTestOpsStore(t)
	ctx := context.Background()
	now := time.Now()
	_ = s.Insert(ctx, Operation{RequestID: "req_hb", Action: "MIGRATE", State: OpStateRunning, SubmittedAt: now})
	at := now.Add(30 * time.Second)
	if err := s.RecordHeartbeat(ctx, "req_hb", at); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}
	got, _ := s.Get(ctx, "req_hb")
	if got.LastHeartbeatAt == nil {
		t.Fatal("LastHeartbeatAt not set")
	}
	if !got.LastHeartbeatAt.Equal(time.Unix(0, at.UnixNano())) {
		t.Fatalf("LastHeartbeatAt = %v, want %v", got.LastHeartbeatAt, at)
	}
}

func TestOpsStore_ListByState(t *testing.T) {
	s, _ := newTestOpsStore(t)
	ctx := context.Background()
	now := time.Now()
	_ = s.Insert(ctx, Operation{RequestID: "a", Action: "X", State: OpStateRunning, SubmittedAt: now.Add(-2 * time.Second)})
	_ = s.Insert(ctx, Operation{RequestID: "b", Action: "X", State: OpStateRunning, SubmittedAt: now})
	_ = s.Insert(ctx, Operation{RequestID: "c", Action: "X", State: OpStateSuccess, SubmittedAt: now})

	got, err := s.ListByState(ctx, OpStateRunning)
	if err != nil {
		t.Fatalf("ListByState: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 running, got %d", len(got))
	}
	if got[0].RequestID != "a" || got[1].RequestID != "b" {
		t.Fatalf("ordering wrong: %s, %s", got[0].RequestID, got[1].RequestID)
	}
}

func TestOpsStore_GCKeepsRunningAndRecent(t *testing.T) {
	s, _ := newTestOpsStore(t)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)
	recent := now.Add(-time.Hour)

	// One old terminal — should be pruned.
	_ = s.Insert(ctx, Operation{RequestID: "old", Action: "X", State: OpStateSuccess, SubmittedAt: old})
	_ = s.UpdateState(ctx, "old", UpdateStateOpts{State: OpStateSuccess, CompletedAt: &old})

	// One recent terminal — should be kept.
	_ = s.Insert(ctx, Operation{RequestID: "recent", Action: "X", State: OpStateSuccess, SubmittedAt: recent})
	_ = s.UpdateState(ctx, "recent", UpdateStateOpts{State: OpStateSuccess, CompletedAt: &recent})

	// One ancient running — should be kept (state is non-terminal).
	_ = s.Insert(ctx, Operation{RequestID: "stuck", Action: "X", State: OpStateRunning, SubmittedAt: old})

	pruned, err := s.GC(ctx, OpsRetentionTerminal)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned %d, want 1", pruned)
	}

	if r, _ := s.Get(ctx, "old"); r != nil {
		t.Fatal("old terminal row should have been pruned")
	}
	if r, _ := s.Get(ctx, "recent"); r == nil {
		t.Fatal("recent terminal row was unexpectedly pruned")
	}
	if r, _ := s.Get(ctx, "stuck"); r == nil {
		t.Fatal("running row should never be pruned")
	}
}

func TestOpsStore_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ops.db")
	s, err := OpenOperationsStore(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	_ = s.Insert(ctx, Operation{RequestID: "persist", Action: "MIGRATE", State: OpStateRunning, SubmittedAt: time.Now()})
	_ = s.Close()

	s2, err := OpenOperationsStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	got, _ := s2.Get(ctx, "persist")
	if got == nil {
		t.Fatal("row lost across reopen")
	}
	if got.State != OpStateRunning {
		t.Fatalf("state = %s, want running", got.State)
	}
}

func TestOpsStore_CorruptionFailsClosedAndPreservesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops.db")
	original := []byte("this is not a sqlite database file")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenOperationsStore(path)
	if err == nil {
		store.Close()
		t.Fatal("corrupt history must not be replaced with an empty operation store")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != string(original) {
		t.Fatalf("original history changed: %q, %v", got, readErr)
	}
}

// TestOutcomeCache_ReadThroughOpsDB: when the in-memory cache has
// no entry but ops.db holds a terminal row, lookup() synthesizes
// the cachedOutcome shape so handleCommandOutcome can serve it.
func TestOutcomeCache_ReadThroughOpsDB(t *testing.T) {
	s, _ := newTestOpsStore(t)
	prevOps := ops
	ops = s
	t.Cleanup(func() { ops = prevOps })

	resetOutcomes(t) // clear in-memory cache

	now := time.Now()
	completed := now.Add(time.Minute)
	if err := s.Insert(context.Background(), Operation{
		RequestID:   "req_rt",
		Action:      "DELETE_SNAPSHOT",
		State:       OpStateSuccess,
		SubmittedAt: now,
		Output:      "snapshot deleted",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.UpdateState(context.Background(), "req_rt", UpdateStateOpts{
		State:       OpStateSuccess,
		Output:      "snapshot deleted",
		CompletedAt: &completed,
	}); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	status, body, _, ok := outcomes.lookup("req_rt")
	if !ok {
		t.Fatal("lookup miss; expected ops.db read-through hit")
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(string(body), "snapshot deleted") {
		t.Fatalf("body = %q (want message)", body)
	}
}

func TestOutcomeCache_ReadThroughSkipsNonTerminal(t *testing.T) {
	s, _ := newTestOpsStore(t)
	prevOps := ops
	ops = s
	t.Cleanup(func() { ops = prevOps })

	resetOutcomes(t)
	if err := s.Insert(context.Background(), Operation{
		RequestID:   "req_running",
		Action:      "MIGRATE",
		State:       OpStateRunning,
		SubmittedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := outcomes.lookup("req_running"); ok {
		t.Fatal("lookup should not surface non-terminal rows")
	}
}

func TestIsTerminalOpState(t *testing.T) {
	for _, s := range []string{OpStateSuccess, OpStateFailed, OpStateCancelled, OpStateUnknown} {
		if !IsTerminalOpState(s) {
			t.Fatalf("%s should be terminal", s)
		}
	}
	for _, s := range []string{OpStatePending, OpStateRunning, "weird"} {
		if IsTerminalOpState(s) {
			t.Fatalf("%s must not be terminal", s)
		}
	}
}

func TestOpsStore_FinishActiveCannotOverwriteTerminal(t *testing.T) {
	s, _ := newTestOpsStore(t)
	ctx := context.Background()
	if err := s.Insert(ctx, Operation{RequestID: "first-terminal", Action: "STOP", State: OpStateRunning}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishActive(ctx, "first-terminal", UpdateStateOpts{State: OpStateCancelled, Error: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishActive(ctx, "first-terminal", UpdateStateOpts{State: OpStateSuccess, Output: "later"}); err == nil {
		t.Fatal("terminal update replaced cancellation")
	}
	got, _ := s.Get(ctx, "first-terminal")
	if got.State != OpStateCancelled || got.Error != "first" || got.Output != "" {
		t.Fatalf("terminal state changed: %+v", got)
	}
}
