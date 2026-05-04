package main

// Async dispatch (v5.48 PR-2e).
//
// Slow actions (DELETE_SNAPSHOT, MIGRATE, OFFLINE_MIGRATE, etc.)
// previously held the server's HTTP call open for the duration —
// minutes for migrations, occasionally tens of minutes for big
// snapshots. The 2026-04-30 host-a incident showed this is the
// architectural fault line: anything taking longer than the
// server's per-action HTTP timeout looked like a failure even
// when libvirt completed the work.
//
// With async dispatch the agent replies 202 Accepted with the
// request_id in <100 ms and runs the body in a goroutine. The
// server's PR-1c reconciler polls /command-outcome to recover the
// terminal outcome from ops.db (PR-2a + PR-2b populated it).
//
// Gated by KVM_AGENT_ASYNC_COMMANDS=true (default off) so an
// operator can roll it out per-host during a v5.48 fleet upgrade
// without having to coordinate the server-side reconciler at the
// same time. Pre-async agents continue to return 200 + body
// synchronously and old servers / kvmctl callers see no change.

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"kvm-center/internal/models"
)

// asyncEligibleActions is the set of actions where the body is
// slow enough that holding the HTTP connection open is a problem.
// Fast actions (START/STOP/RESTART/SNAPSHOT/REVERT_SNAPSHOT)
// continue to return synchronously — they're sub-second.
var asyncEligibleActions = map[string]struct{}{
	"DELETE_SNAPSHOT": {},
	"OFFLINE_MIGRATE": {},
	"DELETE_VM":       {},
	"POWER_CYCLE":     {},
	// Live MIGRATE is intentionally NOT here: it has a migration
	// record in store.migrations whose finalization is wired to the
	// synchronous HTTP reply. Until that lifecycle is async-aware
	// too (post-Phase-2), live MIGRATE keeps running synchronously
	// inside the existing 60-min HTTP timeout.
}

func asyncEligibleAction(action string) bool {
	_, ok := asyncEligibleActions[strings.ToUpper(action)]
	return ok
}

// asyncEnabled returns true when the operator has opted into async
// dispatch via the env var. ops.db must be open too — without it
// we have nowhere to record the outcome and the server's
// reconciler would never resolve the entry.
func asyncEnabled() bool {
	if ops == nil {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("KVM_AGENT_ASYNC_COMMANDS")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// respondAccepted writes the 202 reply that signals "we own this
// now; poll /command-outcome later". Server treats 202 the same as
// pending in command_log.
func respondAccepted(w http.ResponseWriter, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"request_id": requestID,
		"status":     "running",
		"async":      "true",
	})
}

// captureResponseWriter is the substitute we hand the action body
// in async mode. It records what the body would have written so
// the deferred outcome cache + ops.db updates get the same
// information the synchronous path stored.
type captureResponseWriter struct {
	header http.Header
	status int
	body   []byte
}

func newCaptureResponseWriter() *captureResponseWriter {
	return &captureResponseWriter{header: make(http.Header)}
}

func (c *captureResponseWriter) Header() http.Header { return c.header }

func (c *captureResponseWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *captureResponseWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.body = append(c.body, p...)
	return len(p), nil
}

// runCommandBodyAsync is the goroutine entrypoint when async
// dispatch is enabled. It runs the same dispatcher the sync path
// uses, just against a buffered writer, and on completion pushes
// the captured outcome to the cache + ops.db.
//
// Panics inside action bodies are caught and recorded as a
// failure — without this, an async panic would silently leave the
// row in `running` until the boot reconciler resolved it.
func runCommandBodyAsync(req models.CommandRequest) {
	cap := newCaptureResponseWriter()
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("async command panic: req_id=%s action=%s: %v", req.RequestID, req.Action, rec)
			if cap.status == 0 {
				cap.status = http.StatusInternalServerError
			}
			if len(cap.body) == 0 {
				panicBody, _ := json.Marshal(map[string]any{
					"success": false,
					"message": "panic in async dispatch",
				})
				cap.body = panicBody
			}
		}
		if cap.status == 0 {
			cap.status = http.StatusOK
		}
		outcomes.put(req.RequestID, cap.status, cap.body)
		recordOpsLifecycleEnd(req.RequestID, cap.status, cap.body)
	}()
	runCommandBody(cap, req)
}
