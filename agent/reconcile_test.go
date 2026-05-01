package main

// Tests for boot reconciliation + heartbeat (v5.48 PR-2d). The
// libvirt seams are injected so we don't actually need libvirtd in
// CI. The store-level operations all run against a real ops.db
// using newTestOpsStore.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newReconcileDeps(t *testing.T) (reconcileDeps, *fakeProbe) {
	t.Helper()
	p := &fakeProbe{
		state:     map[string]vmState{},
		snapshots: map[string]bool{},
	}
	deps := reconcileDeps{
		domainState:  func(vm string) (vmState, error) { return p.checkState(vm) },
		snapshotHas:  func(vm, name string) (bool, error) { return p.checkSnapshot(vm, name) },
		pidAlive:     func(int) bool { return false },
		now:          func() time.Time { return p.now },
		logf:         func(string, ...any) {},
		errMsgPrefix: "boot-reconcile",
	}
	return deps, p
}

type fakeProbe struct {
	state     map[string]vmState
	snapshots map[string]bool // key "vm/name"
	now       time.Time
	pidAlive  bool
}

func (p *fakeProbe) checkState(vm string) (vmState, error) {
	if s, ok := p.state[vm]; ok {
		return s, nil
	}
	return vmStateOther, nil
}

func (p *fakeProbe) checkSnapshot(vm, name string) (bool, error) {
	return p.snapshots[vm+"/"+name], nil
}

func TestBootReconcile_DeleteSnapshotRecognised(t *testing.T) {
	store := withOpsStore(t)
	deps, probe := newReconcileDeps(t)
	probe.now = time.Now()

	op := Operation{
		RequestID:   "req_ds",
		Action:      "DELETE_SNAPSHOT",
		VMName:      "web01",
		Params:      map[string]string{"name": "snap-old"},
		State:       OpStateRunning,
		SubmittedAt: time.Now().Add(-2 * time.Hour),
	}
	if err := store.Insert(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	// Tonight's bug exactly: the snapshot is gone in libvirt; agent
	// died before it could record success.
	probe.snapshots["web01/snap-old"] = false

	reconcileBootRow(context.Background(), store, &op, deps)

	got, _ := store.Get(context.Background(), "req_ds")
	if got.State != OpStateSuccess {
		t.Fatalf("state = %q, want success (snapshot gone, action completed)", got.State)
	}
	if !strings.Contains(got.Output, "no longer present") {
		t.Fatalf("forensic output missing: %q", got.Output)
	}
}

func TestBootReconcile_DeleteSnapshotStillPresentMarksFailed(t *testing.T) {
	store := withOpsStore(t)
	deps, probe := newReconcileDeps(t)
	probe.now = time.Now()

	op := Operation{
		RequestID: "req_ds_present",
		Action:    "DELETE_SNAPSHOT",
		VMName:    "web01",
		Params:    map[string]string{"name": "snap-present"},
		State:     OpStateRunning,
	}
	_ = store.Insert(context.Background(), op)
	probe.snapshots["web01/snap-present"] = true

	reconcileBootRow(context.Background(), store, &op, deps)

	got, _ := store.Get(context.Background(), "req_ds_present")
	if got.State != OpStateFailed {
		t.Fatalf("state = %q, want failed", got.State)
	}
}

func TestBootReconcile_StartRecognisedAsSuccessIfRunning(t *testing.T) {
	store := withOpsStore(t)
	deps, probe := newReconcileDeps(t)
	probe.now = time.Now()
	op := Operation{RequestID: "req_start", Action: "START", VMName: "vm1", State: OpStateRunning}
	_ = store.Insert(context.Background(), op)
	probe.state["vm1"] = vmStateRunning

	reconcileBootRow(context.Background(), store, &op, deps)
	got, _ := store.Get(context.Background(), "req_start")
	if got.State != OpStateSuccess {
		t.Fatalf("state = %q", got.State)
	}
}

func TestBootReconcile_DeleteVMSuccessWhenMissing(t *testing.T) {
	store := withOpsStore(t)
	deps, probe := newReconcileDeps(t)
	probe.now = time.Now()
	op := Operation{RequestID: "req_dv", Action: "DELETE_VM", VMName: "old-vm", State: OpStateRunning}
	_ = store.Insert(context.Background(), op)
	probe.state["old-vm"] = vmStateMissing

	reconcileBootRow(context.Background(), store, &op, deps)
	got, _ := store.Get(context.Background(), "req_dv")
	if got.State != OpStateSuccess {
		t.Fatalf("state = %q", got.State)
	}
}

func TestBootReconcile_MigrateMarksUnknown(t *testing.T) {
	store := withOpsStore(t)
	deps, probe := newReconcileDeps(t)
	probe.now = time.Now()
	op := Operation{RequestID: "req_mig", Action: "MIGRATE", VMName: "any", State: OpStateRunning}
	_ = store.Insert(context.Background(), op)

	reconcileBootRow(context.Background(), store, &op, deps)
	got, _ := store.Get(context.Background(), "req_mig")
	if got.State != OpStateUnknown {
		t.Fatalf("state = %q, want unknown for ambiguous MIGRATE", got.State)
	}
	if !strings.Contains(got.Error, "MIGRATE") {
		t.Fatalf("forensic note missing: %q", got.Error)
	}
}

func TestBootReconcile_ChildPIDAliveMarksUnknown(t *testing.T) {
	store := withOpsStore(t)
	deps, probe := newReconcileDeps(t)
	probe.now = time.Now()
	deps.pidAlive = func(int) bool { return true }

	op := Operation{
		RequestID: "req_alive",
		Action:    "DELETE_SNAPSHOT",
		VMName:    "web01",
		Params:    map[string]string{"name": "snap-x"},
		State:     OpStateRunning,
		ChildPID:  9999,
	}
	_ = store.Insert(context.Background(), op)
	_ = store.UpdateState(context.Background(), op.RequestID, UpdateStateOpts{ChildPID: 9999})

	reconcileBootRow(context.Background(), store, &op, deps)
	got, _ := store.Get(context.Background(), "req_alive")
	if got.State != OpStateUnknown {
		t.Fatalf("state = %q, want unknown when child still alive", got.State)
	}
	if !strings.Contains(got.Error, "child PID") {
		t.Fatalf("error must mention child PID, got %q", got.Error)
	}
}

func TestReconcileBoot_DrainsAllRunningRows(t *testing.T) {
	store := withOpsStore(t)
	for i, action := range []string{"START", "STOP", "DELETE_VM"} {
		_ = store.Insert(context.Background(), Operation{
			RequestID: "req_" + action,
			Action:    action,
			VMName:    "vm" + string(rune('a'+i)),
			State:     OpStateRunning,
		})
	}
	reconcileBoot(context.Background(), store)

	rows, _ := store.ListByState(context.Background(), OpStateRunning)
	if len(rows) != 0 {
		t.Fatalf("running rows after reconcile: %d (want 0)", len(rows))
	}
}

func TestRunOpsHeartbeat_RefreshesEvery30s(t *testing.T) {
	store := withOpsStore(t)
	op := Operation{RequestID: "req_hb", Action: "MIGRATE", VMName: "vm", State: OpStateRunning, SubmittedAt: time.Now()}
	_ = store.Insert(context.Background(), op)

	beatOnce(context.Background(), store)
	got, _ := store.Get(context.Background(), "req_hb")
	if got.LastHeartbeatAt == nil {
		t.Fatal("heartbeat not stamped")
	}
	if time.Since(*got.LastHeartbeatAt) > time.Minute {
		t.Fatalf("heartbeat too old: %v", got.LastHeartbeatAt)
	}
}

func TestRunOpsHeartbeat_StopsOnContextCancel(t *testing.T) {
	store := withOpsStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runOpsHeartbeat(ctx, store); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat goroutine didn't return after ctx cancel")
	}
}
