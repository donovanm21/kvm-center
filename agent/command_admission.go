package main

import (
	"context"
	"fmt"
	"sync"

	"kvm-center/internal/models"
)

// Serialize the check-and-claim phase, including the degraded in-memory
// mode. Running claims never expire with the completed-outcome cache.
var commandAdmissions = struct {
	sync.Mutex
	running map[string]bool
}{running: make(map[string]bool)}

// claimCommand returns a completed replay, an in-progress acknowledgement,
// or ownership of a newly claimed operation (nil, false, nil).
func claimCommand(req models.CommandRequest) (*cachedOutcome, bool, error) {
	commandAdmissions.Lock()
	defer commandAdmissions.Unlock()
	if e, ok := outcomes.get(req.RequestID); ok {
		return &e, false, nil
	}
	if commandAdmissions.running[req.RequestID] {
		return nil, true, nil
	}
	claimed, err := recordOpsLifecycleStart(req)
	if err != nil {
		return nil, false, err
	}
	if claimed {
		commandAdmissions.running[req.RequestID] = true
		return nil, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), opsLookupDeadline)
	defer cancel()
	op, err := ops.Get(ctx, req.RequestID)
	if err != nil {
		return nil, false, err
	}
	if op == nil {
		return nil, false, fmt.Errorf("existing operation disappeared")
	}
	if status, body, at, ok := synthesizeOutcomeFromOp(op); ok {
		return &cachedOutcome{status: status, body: body, at: at}, false, nil
	}
	if op.State != OpStatePending && op.State != OpStateRunning {
		return nil, false, fmt.Errorf("unrecognized operation state %q", op.State)
	}
	return nil, true, nil
}
