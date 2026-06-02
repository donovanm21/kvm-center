package main

// Boot reconciliation + heartbeat for the v5.48 ops.db substrate.
//
// On agent startup, any row in `running` state is from a prior
// agent process that was killed mid-action. We resolve each one by:
//
//   1. Checking /proc for the recorded child_pid:
//      - alive  → mark `unknown` with a forensic note (the action
//        may complete on its own; libvirt is the truth — we don't
//        try to reattach the watchdog because that's a portability
//        nightmare across restarts);
//      - gone   → query libvirt for the action's expected post-state
//        and resolve to `success`/`failed`/`unknown` accordingly.
//
// While the agent is running, every row in `running` state has its
// `last_heartbeat_at` refreshed every 30 s by a single background
// goroutine. The server-side reconciler (PR-1c) and a future
// "agent went silent" detector both consume the heartbeat.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// heartbeatInterval is how often the agent refreshes
// last_heartbeat_at on rows in `running` state. 30 s matches spec
// §3 — fresh enough for the boot reconciler's 90 s freshness
// threshold, infrequent enough to be cheap.
const heartbeatInterval = 30 * time.Second

// runOpsHeartbeat refreshes last_heartbeat_at on every running
// row at heartbeatInterval. Returns when ctx is cancelled.
func runOpsHeartbeat(ctx context.Context, store *OperationsStore) {
	if store == nil {
		return
	}
	tick := time.NewTicker(heartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			beatOnce(ctx, store)
		}
	}
}

func beatOnce(ctx context.Context, store *OperationsStore) {
	rows, err := store.ListByState(ctx, OpStateRunning)
	if err != nil {
		log.Printf("ops.db: heartbeat list: %v", err)
		return
	}
	now := time.Now()
	for _, r := range rows {
		_ = store.RecordHeartbeat(ctx, r.RequestID, now)
	}
}

// virshChecker is the seam used by reconcileBootRow to ask libvirt
// for the current state of a VM. Production wires it to virsh;
// tests inject a stub.
type virshChecker func(vmTarget string) (state vmState, err error)

// vmState is the subset of libvirt's domstate output the
// reconciler cares about. We don't need the full VMState enum
// because reconciliation only branches on running/stopped/missing.
type vmState int

const (
	vmStateMissing vmState = iota
	vmStateStopped
	vmStateRunning
	vmStateOther
)

// realVirshState shells out to `virsh domstate <vm>`. Returns
// missing when the VM doesn't exist (virsh exits non-zero with
// "error: failed to get domain"). Anything else gets parsed.
func realVirshState(vm string) (vmState, error) {
	if vm == "" {
		return vmStateMissing, nil
	}
	out, err := exec.Command("virsh", "domstate", vm).CombinedOutput()
	if err != nil {
		// Heuristic: virsh prints "error: failed to get domain" or
		// "Domain not found" when the VM isn't defined. Treat as
		// missing rather than bubbling the error up — the caller
		// usually wants to distinguish "VM gone" from real failure.
		s := strings.ToLower(string(out))
		if strings.Contains(s, "failed to get domain") || strings.Contains(s, "not found") {
			return vmStateMissing, nil
		}
		return vmStateOther, fmt.Errorf("virsh domstate %s: %w", vm, err)
	}
	switch strings.TrimSpace(string(out)) {
	case "running":
		return vmStateRunning, nil
	case "shut off", "shutoff":
		return vmStateStopped, nil
	}
	return vmStateOther, nil
}

// snapshotChecker reports whether snapshot `name` exists on `vm`.
// Production shells out; tests inject.
type snapshotChecker func(vm, name string) (exists bool, err error)

func realVirshSnapshotExists(vm, name string) (bool, error) {
	if vm == "" || name == "" {
		return false, nil
	}
	out, err := exec.Command("virsh", "snapshot-info", "--domain", vm, "--snapshotname", name).CombinedOutput()
	if err != nil {
		s := strings.ToLower(string(out))
		if strings.Contains(s, "not found") || strings.Contains(s, "no snapshot") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// reconcileDeps lets tests inject the libvirt seams.
type reconcileDeps struct {
	domainState  virshChecker
	snapshotHas  snapshotChecker
	pidAlive     func(pid int) bool
	now          func() time.Time
	logf         func(format string, args ...any)
	errMsgPrefix string
}

func defaultReconcileDeps() reconcileDeps {
	return reconcileDeps{
		domainState:  realVirshState,
		snapshotHas:  realVirshSnapshotExists,
		pidAlive:     pidExists,
		now:          time.Now,
		logf:         log.Printf,
		errMsgPrefix: "boot-reconcile",
	}
}

// pidExists returns true if /proc/<pid> exists (Linux). On macOS /
// other dev hosts the agent never runs in production, but for unit
// tests we still want a real process check, so we look up the
// process via os.FindProcess + signal-zero (POSIX-portable).
func pidExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Sending signal 0 is the POSIX "is this PID alive?" probe.
	if err := p.Signal(syscallSignalZero()); err != nil {
		return false
	}
	return true
}

// reconcileBoot resolves every running row left over from a prior
// agent process. Called once from main() right after ops.db is
// open + before the command server starts accepting requests.
func reconcileBoot(ctx context.Context, store *OperationsStore) {
	if store == nil {
		return
	}
	deps := defaultReconcileDeps()
	rows, err := store.ListByState(ctx, OpStateRunning)
	if err != nil {
		deps.logf("%s: list running: %v", deps.errMsgPrefix, err)
		return
	}
	if len(rows) == 0 {
		return
	}
	deps.logf("%s: resolving %d in-flight rows from prior agent process", deps.errMsgPrefix, len(rows))
	for _, r := range rows {
		reconcileBootRow(ctx, store, r, deps)
	}
}

// reconcileBootRow drives a single running row to terminal.
func reconcileBootRow(ctx context.Context, store *OperationsStore, op *Operation, deps reconcileDeps) {
	now := deps.now()
	// Case 1: child still alive — agent restart didn't take the
	// child with it (rare; usually Pdeathsig kills it). Mark
	// unknown so the operator decides.
	if op.ChildPID > 0 && deps.pidAlive(op.ChildPID) {
		fresh := op.LastHeartbeatAt != nil && now.Sub(*op.LastHeartbeatAt) < 90*time.Second
		_ = fresh // PR-2d: log only; reattach is out of scope.
		msg := fmt.Sprintf("agent restarted while %s was in flight; child PID %d still alive — review manually", op.Action, op.ChildPID)
		_ = store.UpdateState(ctx, op.RequestID, UpdateStateOpts{
			State:       OpStateUnknown,
			Error:       msg,
			CompletedAt: &now,
		})
		deps.logf("%s: %s req_id=%s pid=%d → unknown (child alive)", deps.errMsgPrefix, op.Action, op.RequestID, op.ChildPID)
		return
	}

	state, output, errStr := classifyByLibvirt(op, deps)
	completedAt := now
	if op.LastHeartbeatAt != nil && op.LastHeartbeatAt.After(completedAt) {
		completedAt = *op.LastHeartbeatAt
	}
	_ = store.UpdateState(ctx, op.RequestID, UpdateStateOpts{
		State:         state,
		Output:        output,
		Error:         errStr,
		ClearChildPID: true,
		CompletedAt:   &completedAt,
	})
	deps.logf("%s: %s req_id=%s vm=%s → %s", deps.errMsgPrefix, op.Action, op.RequestID, op.VMName, state)
}

// classifyByLibvirt maps an in-flight action + the VM's current
// libvirt state to a terminal outcome.
//
// We aim for high-confidence verdicts on the actions where libvirt
// is unambiguous; everything else gets `unknown` with a forensic
// note so the operator sees it. False positives ("we said success
// but the action didn't really apply") are worse than false
// unknowns ("we said unknown, operator looks").
func classifyByLibvirt(op *Operation, deps reconcileDeps) (state, output, errMsg string) {
	target := op.VMName
	if target == "" {
		target = op.VMUUID
	}
	switch strings.ToUpper(op.Action) {
	case "START":
		s, err := deps.domainState(target)
		if err != nil {
			return OpStateUnknown, "", "boot-reconcile: virsh probe failed: " + err.Error()
		}
		if s == vmStateRunning {
			return OpStateSuccess, "VM is running per libvirt; agent restarted mid-START", ""
		}
		return OpStateFailed, "", "VM not running per libvirt; START did not apply"
	case "STOP", "FORCESTOP":
		s, err := deps.domainState(target)
		if err != nil {
			return OpStateUnknown, "", "boot-reconcile: virsh probe failed: " + err.Error()
		}
		if s == vmStateStopped || s == vmStateMissing {
			return OpStateSuccess, "VM is stopped per libvirt; agent restarted mid-STOP", ""
		}
		return OpStateFailed, "", "VM still running per libvirt; STOP did not apply"
	case "DELETE_VM":
		s, err := deps.domainState(target)
		if err != nil {
			return OpStateUnknown, "", "boot-reconcile: virsh probe failed: " + err.Error()
		}
		if s == vmStateMissing {
			return OpStateSuccess, "VM no longer defined; agent restarted mid-DELETE_VM", ""
		}
		return OpStateUnknown, "", "VM still defined per libvirt; DELETE_VM partial; review"
	case "DELETE_SNAPSHOT":
		name := op.Params["name"]
		if name == "" {
			return OpStateUnknown, "", "DELETE_SNAPSHOT lacked name param; cannot resolve"
		}
		exists, err := deps.snapshotHas(target, name)
		if err != nil {
			return OpStateUnknown, "", "boot-reconcile: virsh snapshot probe failed: " + err.Error()
		}
		if !exists {
			return OpStateSuccess, "snapshot " + name + " no longer present; agent restarted mid-DELETE_SNAPSHOT", ""
		}
		return OpStateFailed, "", "snapshot " + name + " still present; DELETE_SNAPSHOT did not complete"
	case "SNAPSHOT":
		name := op.Params["name"]
		if name == "" {
			return OpStateUnknown, "", "SNAPSHOT lacked name param; cannot resolve"
		}
		exists, err := deps.snapshotHas(target, name)
		if err != nil {
			return OpStateUnknown, "", "boot-reconcile: virsh snapshot probe failed: " + err.Error()
		}
		if exists {
			return OpStateSuccess, "snapshot " + name + " present; agent restarted mid-SNAPSHOT", ""
		}
		return OpStateFailed, "", "snapshot " + name + " not present; SNAPSHOT did not apply"
	}
	// Anything else — MIGRATE, OFFLINE_MIGRATE, EDIT_VM,
	// CHANGE_MEDIA, REVERT_SNAPSHOT, POWER_CYCLE — has a multi-step
	// effect that libvirt's instantaneous state can't unambiguously
	// reveal. Mark unknown with a clear note.
	return OpStateUnknown, "", "agent restarted mid-" + strings.ToUpper(op.Action) + "; manual review required"
}

// errAlreadyTerminal is returned when reconcileBootRow finds the
// row already in a terminal state (idempotent re-run, race with a
// late agent write). Callers ignore it.
var errAlreadyTerminal = errors.New("ops.db row already terminal")
