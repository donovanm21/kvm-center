package main

// Tests for the v5.48 PR-2b action lifecycle wrapper. Every command
// the agent processes should leave a terminal row in ops.db keyed
// on its request_id. We don't actually shell out to virsh in tests
// — handleCommand routes through `exec.Command` which we can't
// mock cleanly without a heavy refactor. Instead, we test the
// lifecycle helpers directly with a synthetic CommandResponse body
// and verify the ops.db transitions are correct.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func withOpsStore(t *testing.T) *OperationsStore {
	t.Helper()
	s, _ := newTestOpsStore(t)
	prev := ops
	ops = s
	t.Cleanup(func() { ops = prev })
	return s
}

func TestLifecycle_StartInsertsRunningRow(t *testing.T) {
	s := withOpsStore(t)

	req := models.CommandRequest{
		RequestID: "req_lc1",
		Action:    "START",
		VMName:    "web01",
		VMUUID:    "uuid-1",
		Params:    map[string]string{"foo": "bar"},
	}
	recordOpsLifecycleStart(req)

	got, err := s.Get(context.Background(), "req_lc1")
	if err != nil || got == nil {
		t.Fatalf("ops.db row missing after lifecycle start: err=%v", err)
	}
	if got.State != OpStateRunning {
		t.Fatalf("state=%s, want running", got.State)
	}
	if got.Action != "START" || got.VMName != "web01" || got.VMUUID != "uuid-1" {
		t.Fatalf("metadata not stored: %+v", got)
	}
	if got.StartedAt == nil {
		t.Fatal("StartedAt should be stamped on lifecycle start")
	}
	if got.Params["foo"] != "bar" {
		t.Fatalf("params not roundtripped: %+v", got.Params)
	}
}

func TestLifecycle_EndMarksSuccessFromBody(t *testing.T) {
	s := withOpsStore(t)
	req := models.CommandRequest{RequestID: "req_lc_ok", Action: "STOP", VMName: "web01"}
	recordOpsLifecycleStart(req)

	body, _ := json.Marshal(map[string]any{
		"success": true,
		"message": "domain is being shutdown",
	})
	recordOpsLifecycleEnd("req_lc_ok", 200, body)

	got, _ := s.Get(context.Background(), "req_lc_ok")
	if got.State != OpStateSuccess {
		t.Fatalf("state=%s, want success", got.State)
	}
	if got.Output != "domain is being shutdown" {
		t.Fatalf("output=%q", got.Output)
	}
	if got.Error != "" {
		t.Fatalf("error should be empty on success, got %q", got.Error)
	}
	if got.CompletedAt == nil {
		t.Fatal("CompletedAt must be set on terminal")
	}
}

func TestLifecycle_EndMarksFailedFromError(t *testing.T) {
	s := withOpsStore(t)
	req := models.CommandRequest{RequestID: "req_lc_fail", Action: "MIGRATE"}
	recordOpsLifecycleStart(req)

	body, _ := json.Marshal(map[string]any{
		"success": false,
		"message": "live migration",
		"error":   "virsh: connection refused",
	})
	recordOpsLifecycleEnd("req_lc_fail", 500, body)

	got, _ := s.Get(context.Background(), "req_lc_fail")
	if got.State != OpStateFailed {
		t.Fatalf("state=%s, want failed", got.State)
	}
	if got.Error != "virsh: connection refused" {
		t.Fatalf("error=%q", got.Error)
	}
}

func TestLifecycle_EndHandlesNonJSONBody(t *testing.T) {
	s := withOpsStore(t)
	req := models.CommandRequest{RequestID: "req_lc_raw", Action: "START"}
	recordOpsLifecycleStart(req)
	recordOpsLifecycleEnd("req_lc_raw", 500, []byte("plain text error from upstream"))

	got, _ := s.Get(context.Background(), "req_lc_raw")
	if got.State != OpStateFailed || got.Error != "plain text error from upstream" {
		t.Fatalf("got=%+v", got)
	}
}

func TestLifecycle_NoOpWhenOpsDBClosed(t *testing.T) {
	prev := ops
	ops = nil
	defer func() { ops = prev }()
	// Must not panic.
	recordOpsLifecycleStart(models.CommandRequest{RequestID: "req_x", Action: "START"})
	recordOpsLifecycleEnd("req_x", 200, []byte(`{"success":true}`))
}

func TestLifecycle_NoOpWhenRequestIDEmpty(t *testing.T) {
	s := withOpsStore(t)
	recordOpsLifecycleStart(models.CommandRequest{Action: "START"})
	rows, _ := s.ListByState(context.Background(), OpStateRunning)
	if len(rows) != 0 {
		t.Fatalf("empty request_id should not produce a row, got %d", len(rows))
	}
}

func TestLifecycle_StartIdempotent(t *testing.T) {
	s := withOpsStore(t)
	req := models.CommandRequest{RequestID: "req_dup", Action: "START"}
	recordOpsLifecycleStart(req)
	recordOpsLifecycleStart(req) // second call is a no-op on Insert
	rows, _ := s.ListByState(context.Background(), OpStateRunning)
	if len(rows) != 1 {
		t.Fatalf("idempotent insert produced %d rows", len(rows))
	}
}

func TestLifecycle_EndCompletedAtAfterStart(t *testing.T) {
	s := withOpsStore(t)
	req := models.CommandRequest{RequestID: "req_dur", Action: "STOP"}
	recordOpsLifecycleStart(req)
	time.Sleep(2 * time.Millisecond) // make completed_at strictly after started_at
	recordOpsLifecycleEnd("req_dur", 200, []byte(`{"success":true}`))
	got, _ := s.Get(context.Background(), "req_dur")
	if got.StartedAt == nil || got.CompletedAt == nil {
		t.Fatal("both timestamps should be set")
	}
	if !got.CompletedAt.After(*got.StartedAt) {
		t.Fatalf("CompletedAt %v should be after StartedAt %v", got.CompletedAt, got.StartedAt)
	}
}
