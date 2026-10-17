package main

// Operator-driven command cancellation (v5.48 PR-2f).
//
// `POST /command/cancel?request_id=<id>` looks up the running
// operation in ops.db, sends a cancel signal to the underlying
// virsh / qemu-img child, and marks the row `cancelled` with the
// operator's reason. Idempotent: cancelling an already-terminal
// row returns 200 with a "already <state>" message.
//
// For migrations we also invoke `virsh domjobabort <vm>` because
// `virsh migrate --verbose` doesn't always respond to SIGTERM
// cleanly when libvirtd has its own state machine in flight.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// cancelDeps lets tests inject the kill + domjobabort seams.
type cancelDeps struct {
	signal       func(pid int, sig syscall.Signal) error
	pidAlive     func(pid int) bool
	domjobAbort  func(vm string) error
	now          func() time.Time
	logf         func(format string, args ...any)
	graceSeconds int
}

func defaultCancelDeps() cancelDeps {
	return cancelDeps{
		signal: func(pid int, sig syscall.Signal) error {
			if pid <= 0 {
				return errors.New("cancel: zero pid")
			}
			p, err := os.FindProcess(pid)
			if err != nil {
				return err
			}
			return p.Signal(sig)
		},
		pidAlive: pidExists,
		domjobAbort: func(vm string) error {
			if vm == "" {
				return errors.New("cancel: empty vm")
			}
			return exec.Command("virsh", "domjobabort", vm).Run()
		},
		now:          time.Now,
		logf:         log.Printf,
		graceSeconds: watchdogGraceSeconds,
	}
}

// handleCancelCommand handles POST /command/cancel?request_id=<id>.
// Body is optional — if present, may carry `{"reason": "..."}` to
// stamp into the audit + ops.db row.
func handleCancelCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("request_id")
	if id == "" {
		respond(w, false, "missing request_id")
		return
	}

	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "cancelled by operator"
	}

	if ops == nil {
		// No persistent store → nothing to cancel against. Tell the
		// caller cleanly so they can fall back to manual intervention.
		respond(w, false, "ops.db unavailable; cannot resolve cancellation target")
		return
	}

	state, err := cancelOperation(r.Context(), id, reason, defaultCancelDeps())
	if err != nil {
		respond(w, false, "cancel failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"request_id": id,
		"state":      state,
		"reason":     reason,
	})
}

// cancelOperation drives a single operation to a terminal
// `cancelled` state. Idempotent — returning the existing terminal
// state if the row is already done. Returns the post-cancel state.
func cancelOperation(ctx context.Context, requestID, reason string, deps cancelDeps) (string, error) {
	op, err := ops.Get(ctx, requestID)
	if err != nil {
		return "", err
	}
	if op == nil {
		return "", fmt.Errorf("unknown request_id %q", requestID)
	}
	if IsTerminalOpState(op.State) {
		// Idempotent: someone else got here first (operator
		// double-click, automation retry).
		deps.logf("cancel: req_id=%s already terminal (%s); idempotent no-op", requestID, op.State)
		return op.State, nil
	}

	if err := ops.requestCancellation(ctx, requestID); err != nil {
		return "", err
	}

	// For live + offline migrations, asking libvirtd to abort is
	// kinder than SIGKILLing virsh out from under it — gives qemu
	// a chance to clean up the migration state machine.
	switch strings.ToUpper(op.Action) {
	case "MIGRATE", "OFFLINE_MIGRATE":
		target := op.VMName
		if target == "" {
			target = op.VMUUID
		}
		if err := deps.domjobAbort(target); err != nil {
			deps.logf("cancel: virsh domjobabort %s req_id=%s: %v (continuing with SIGTERM path)", target, requestID, err)
		}
	}

	// SIGTERM the recorded child PID (if any), give it the standard
	// grace, then SIGKILL.
	if op.ChildPID > 0 {
		if err := deps.signal(op.ChildPID, syscall.SIGTERM); err != nil {
			deps.logf("cancel: SIGTERM pid=%d req_id=%s: %v", op.ChildPID, requestID, err)
		}
		deadline := deps.now().Add(time.Duration(deps.graceSeconds) * time.Second)
		for deps.now().Before(deadline) {
			if !deps.pidAlive(op.ChildPID) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if deps.pidAlive(op.ChildPID) {
			deps.logf("cancel: SIGKILL pid=%d req_id=%s (grace expired)", op.ChildPID, requestID)
			_ = deps.signal(op.ChildPID, syscall.SIGKILL)
		}
	}

	// Signalling is only a request. Retain PID and omit completed_at until
	// the owning executor actually returns; process disappearance is not proof.
	if _, err := ops.db.ExecContext(ctx, `UPDATE operations SET error=? WHERE request_id=? AND state IN (?,?)`, reason, requestID, OpStatePending, OpStateRunning); err != nil {
		return "", err
	}
	current, err := ops.Get(ctx, requestID)
	if err != nil {
		return "", err
	}
	if current != nil && IsTerminalOpState(current.State) {
		return current.State, nil
	}
	return "cancellation_requested", nil
}
