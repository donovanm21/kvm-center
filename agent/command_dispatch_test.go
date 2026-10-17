package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func dispatchForTest(req models.CommandRequest, execute func(http.ResponseWriter, models.CommandRequest)) *httptest.ResponseRecorder {
	body, _ := json.Marshal(req)
	rr := httptest.NewRecorder()
	handleCommandWithExecutor(rr, httptest.NewRequest(http.MethodPost, "/command", bytes.NewReader(body)), execute)
	return rr
}

func TestCommandDispatch_ConcurrentRetriesExecuteOnce(t *testing.T) {
	for _, mode := range []string{"durable-sync", "durable-async"} {
		t.Run(mode, func(t *testing.T) {
			resetOutcomes(t)
			if mode == "memory" {
				prev := ops
				ops = nil
				t.Cleanup(func() { ops = prev })
			} else {
				withOpsStore(t)
			}
			t.Setenv("KVM_AGENT_ASYNC_COMMANDS", "false")
			if mode == "durable-async" {
				t.Setenv("KVM_AGENT_ASYNC_COMMANDS", "true")
			}
			req := models.CommandRequest{RequestID: "concurrent-" + mode, Action: "DELETE_VM", VMName: "test"}
			started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			execute := func(w http.ResponseWriter, _ models.CommandRequest) {
				if calls.Add(1) == 1 {
					close(started)
				}
				<-release
				respond(w, true, "done")
			}
			go func() { dispatchForTest(req, execute); close(finished) }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("command did not start")
			}
			var retries sync.WaitGroup
			for i := 0; i < 12; i++ {
				retries.Add(1)
				go func() { defer retries.Done(); dispatchForTest(req, execute) }()
			}
			// Retries must return immediately while the first execution is blocked.
			returned := make(chan struct{})
			go func() { retries.Wait(); close(returned) }()
			select {
			case <-returned:
			case <-time.After(250 * time.Millisecond):
			}
			close(release)
			<-finished
			<-returned
			// Async completion must finish before test globals are restored.
			deadline := time.Now().Add(3 * time.Second)
			for {
				commandAdmissions.Lock()
				_, cached := outcomes.get(req.RequestID)
				finished := !commandAdmissions.running[req.RequestID]
				commandAdmissions.Unlock()
				if cached && finished {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("outcome not recorded")
				}
				time.Sleep(time.Millisecond)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("command executed %d times, want 1", got)
			}
		})
	}
}

func TestCommandDispatch_ReplaysAfterRestart(t *testing.T) {
	s, path := newTestOpsStore(t)
	req := models.CommandRequest{RequestID: "restart", Action: "START", VMName: "test"}
	if err := s.Insert(context.Background(), Operation{RequestID: req.RequestID, Action: req.Action, VMName: req.VMName, State: OpStateSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenOperationsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	previous := ops
	ops = reopened
	defer func() { ops = previous }()
	resetOutcomes(t)
	var calls int
	rr := dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) { calls++; respond(w, false, "executed again") })
	if calls != 0 || rr.Code != http.StatusOK || rr.Header().Get("X-Command-Replayed") != "true" {
		t.Fatalf("durable replay: executions=%d status=%d headers=%v", calls, rr.Code, rr.Header())
	}
	op, _ := reopened.Get(context.Background(), req.RequestID)
	if op.State != OpStateSuccess {
		t.Fatalf("terminal state changed: %s", op.State)
	}
}

func TestCommandDispatch_DurablePendingRetryDoesNotExecute(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	req := models.CommandRequest{RequestID: "pending-retry", Action: "START", VMName: "test"}
	if err := s.Insert(context.Background(), Operation{RequestID: req.RequestID, Action: req.Action, VMName: req.VMName, State: OpStateRunning}); err != nil {
		t.Fatal(err)
	}
	var calls int
	rr := dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) { calls++; respond(w, true, "unexpected") })
	if calls != 0 || rr.Code != http.StatusAccepted {
		t.Fatalf("pending retry: executions=%d status=%d", calls, rr.Code)
	}
}

func TestCommandDispatch_DatabaseFailureDoesNotExecute(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	_ = s.Close()
	var calls int
	rr := dispatchForTest(models.CommandRequest{RequestID: "db-failure", Action: "START", VMName: "test"}, func(w http.ResponseWriter, _ models.CommandRequest) { calls++; respond(w, true, "unexpected") })
	if calls != 0 || rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed claim: executions=%d status=%d", calls, rr.Code)
	}
}

func TestLifecycle_StartDoesNotReopenTerminalOperation(t *testing.T) {
	s := withOpsStore(t)
	req := models.CommandRequest{RequestID: "terminal", Action: "STOP"}
	recordOpsLifecycleStart(req)
	recordOpsLifecycleEnd(req.RequestID, http.StatusOK, []byte(`{"success":true,"message":"done"}`))
	recordOpsLifecycleStart(req)
	op, _ := s.Get(context.Background(), req.RequestID)
	if op.State != OpStateSuccess {
		t.Fatalf("terminal state reopened: %s", op.State)
	}
}

func TestCommandDispatch_PanicAfterResponseIsFailure(t *testing.T) {
	withOpsStore(t)
	resetOutcomes(t)
	req := models.CommandRequest{RequestID: "panic-after-write", Action: "START", VMName: "test"}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("uncaught panic: %v", r)
		}
	}()
	rr := dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) { respond(w, true, "partial"); panic("test") })
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("panic status=%d", rr.Code)
	}
	op, _ := ops.Get(context.Background(), req.RequestID)
	if op.State != OpStateFailed {
		t.Fatalf("panic persisted as %s", op.State)
	}
}

func TestCommandDispatch_CancelledOutcomeWinsOverLateSuccess(t *testing.T) {
	s := withOpsStore(t)
	resetOutcomes(t)
	req := models.CommandRequest{RequestID: "cancel-wins", Action: "STOP", VMName: "test"}
	rr := dispatchForTest(req, func(w http.ResponseWriter, _ models.CommandRequest) {
		if err := s.FinishActive(context.Background(), req.RequestID, UpdateStateOpts{State: OpStateCancelled, Error: "operator cancelled"}); err != nil {
			t.Fatal(err)
		}
		respond(w, true, "late success")
	})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("cancelled command returned status=%d", rr.Code)
	}
	cached, ok := outcomes.get(req.RequestID)
	if !ok || cached.status != http.StatusInternalServerError || !bytes.Contains(cached.body, []byte("operator cancelled")) {
		t.Fatalf("cache contradicted cancellation: %+v", cached)
	}
}
