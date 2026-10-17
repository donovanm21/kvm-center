package main

// Tests for the v5.48 PR-2f cancellation handler.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func cancelTestDeps(t *testing.T) (*cancelDeps, *cancelStubs) {
	t.Helper()
	stubs := &cancelStubs{
		alive:    map[int]*atomic.Bool{},
		domabort: map[string]int{},
	}
	d := cancelDeps{
		signal: func(pid int, sig syscall.Signal) error {
			stubs.signals.Add(1)
			if sig == syscall.SIGKILL {
				if a, ok := stubs.alive[pid]; ok {
					a.Store(false)
				}
			}
			return nil
		},
		pidAlive: func(pid int) bool {
			if a, ok := stubs.alive[pid]; ok {
				return a.Load()
			}
			return false
		},
		domjobAbort: func(vm string) error {
			stubs.domabort[vm]++
			return nil
		},
		now:          time.Now,
		logf:         func(string, ...any) {},
		graceSeconds: 1,
	}
	return &d, stubs
}

type cancelStubs struct {
	signals  atomic.Int32
	alive    map[int]*atomic.Bool
	domabort map[string]int
}

func TestCancelOperation_TerminalIsIdempotent(t *testing.T) {
	s := withOpsStore(t)
	op := Operation{RequestID: "req_done", Action: "STOP", VMName: "x", State: OpStateSuccess, SubmittedAt: time.Now()}
	completed := time.Now()
	_ = s.Insert(context.Background(), op)
	_ = s.UpdateState(context.Background(), op.RequestID, UpdateStateOpts{State: OpStateSuccess, CompletedAt: &completed})

	deps, _ := cancelTestDeps(t)
	state, err := cancelOperation(context.Background(), "req_done", "operator", *deps)
	if err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
	if state != OpStateSuccess {
		t.Fatalf("state = %s, want success preserved", state)
	}
}

func TestCancelOperation_SIGTERMWithChildPID(t *testing.T) {
	s := withOpsStore(t)
	pid := 12345
	op := Operation{RequestID: "req_run", Action: "DELETE_SNAPSHOT", VMName: "vm", State: OpStateRunning, ChildPID: pid, SubmittedAt: time.Now()}
	_ = s.Insert(context.Background(), op)
	_ = s.UpdateState(context.Background(), op.RequestID, UpdateStateOpts{State: OpStateRunning, ChildPID: pid})

	deps, stubs := cancelTestDeps(t)
	alive := &atomic.Bool{}
	alive.Store(true)
	stubs.alive[pid] = alive

	go func() {
		time.Sleep(150 * time.Millisecond)
		alive.Store(false)
	}()

	state, err := cancelOperation(context.Background(), "req_run", "stuck", *deps)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if state != "cancellation_requested" {
		t.Fatalf("state = %s, want cancelled", state)
	}
	if stubs.signals.Load() == 0 {
		t.Fatal("expected at least one signal sent")
	}
	got, _ := s.Get(context.Background(), "req_run")
	if got.State != OpStateRunning || got.Error != "stuck" {
		t.Fatalf("ops.db row not marked cancelled: %+v", got)
	}
	if got.ChildPID != pid || got.CompletedAt != nil {
		t.Fatalf("PID and completion must remain unresolved post-cancel, got %d", got.ChildPID)
	}
}

func TestCancelOperation_MigrationCallsDomjobabort(t *testing.T) {
	s := withOpsStore(t)
	op := Operation{RequestID: "req_mig", Action: "MIGRATE", VMName: "biggun", State: OpStateRunning, SubmittedAt: time.Now()}
	_ = s.Insert(context.Background(), op)
	_ = s.UpdateState(context.Background(), op.RequestID, UpdateStateOpts{State: OpStateRunning})

	deps, stubs := cancelTestDeps(t)
	state, err := cancelOperation(context.Background(), "req_mig", "convergence_stuck", *deps)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if state != "cancellation_requested" {
		t.Fatalf("state = %s", state)
	}
	if stubs.domabort["biggun"] != 1 {
		t.Fatalf("domjobabort should fire once for migration cancel; got %d", stubs.domabort["biggun"])
	}
}

func TestCancelOperation_UnknownReturnsError(t *testing.T) {
	withOpsStore(t)
	deps, _ := cancelTestDeps(t)
	_, err := cancelOperation(context.Background(), "nope", "x", *deps)
	if err == nil {
		t.Fatal("expected error for unknown request_id")
	}
	if !strings.Contains(err.Error(), "unknown request_id") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandleCancelCommand_HappyPath(t *testing.T) {
	s := withOpsStore(t)
	op := Operation{RequestID: "req_h", Action: "DELETE_VM", VMName: "vm", State: OpStateRunning, SubmittedAt: time.Now()}
	_ = s.Insert(context.Background(), op)
	_ = s.UpdateState(context.Background(), op.RequestID, UpdateStateOpts{State: OpStateRunning})

	r := httptest.NewRequest(http.MethodPost, "/command/cancel?request_id=req_h", strings.NewReader(`{"reason":"test"}`))
	rr := httptest.NewRecorder()
	handleCancelCommand(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got["state"] != "cancellation_requested" {
		t.Fatalf("body = %+v", got)
	}
	row, _ := s.Get(context.Background(), "req_h")
	if row.State != OpStateRunning || row.Error != "test" {
		t.Fatalf("ops.db row: %+v", row)
	}
}

func TestHandleCancelCommand_MissingIDReturns400(t *testing.T) {
	withOpsStore(t)
	r := httptest.NewRequest(http.MethodPost, "/command/cancel", nil)
	rr := httptest.NewRecorder()
	handleCancelCommand(rr, r)
	if rr.Code == http.StatusOK {
		t.Fatal("missing request_id must not return 200")
	}
}

func TestRecordOpsLifecycleEnd_RespectsCancelled(t *testing.T) {
	s := withOpsStore(t)
	completed := time.Now()
	_ = s.Insert(context.Background(), Operation{
		RequestID:   "req_cancel_first",
		Action:      "DELETE_SNAPSHOT",
		State:       OpStateCancelled,
		SubmittedAt: time.Now(),
	})
	_ = s.UpdateState(context.Background(), "req_cancel_first", UpdateStateOpts{
		State:       OpStateCancelled,
		Error:       "operator",
		CompletedAt: &completed,
	})

	// The async goroutine's deferred end fires, simulating the
	// SIGKILLed body returning a 500. Must not overwrite cancelled.
	recordOpsLifecycleEnd("req_cancel_first", 500, []byte(`{"success":false,"error":"interrupted"}`))

	got, _ := s.Get(context.Background(), "req_cancel_first")
	if got.State != OpStateCancelled || got.Error != "operator" {
		t.Fatalf("cancelled state was overwritten by lifecycle end: %+v", got)
	}
}

// Sanity: errAlreadyTerminal isn't yet used externally but exists
// in the package — keep the symbol referenced so the linter
// doesn't trip if we ever wire it up. Unrelated to this PR but
// quiets the unused warning for the time being.
var _ = errAlreadyTerminal

// dummy compile-time check that signal const exists.
var _ = errors.New

func TestCancelOperation_ConcurrentCompletionIsPreserved(t *testing.T) {
	s := withOpsStore(t)
	reqID := "complete-during-cancel"
	if err := s.Insert(context.Background(), Operation{RequestID: reqID, Action: "MIGRATE", VMName: "test", State: OpStateRunning}); err != nil {
		t.Fatal(err)
	}
	deps, _ := cancelTestDeps(t)
	deps.domjobAbort = func(string) error {
		return s.FinishActive(context.Background(), reqID, UpdateStateOpts{State: OpStateSuccess, Output: "completed first"})
	}
	state, err := cancelOperation(context.Background(), reqID, "late cancellation", *deps)
	if err != nil || state != OpStateSuccess {
		t.Fatalf("concurrent completion lost: state=%s err=%v", state, err)
	}
	got, _ := s.Get(context.Background(), reqID)
	if got.State != OpStateSuccess || got.Output != "completed first" || got.Error != "" {
		t.Fatalf("terminal state changed: %+v", got)
	}
}
