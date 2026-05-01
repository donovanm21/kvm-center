package main

// Live command-log reconciler (v5.47.2+).
//
// Background goroutine that resolves command-log entries left
// "pending" by handleCommand when its initial POST /command call
// hit a transport error / timeout / 5xx. We poll the agent's
// GET /command-outcome (PR-1b) to recover the actual outcome from
// its in-memory cache. Salvages the bookkeeping-divergence bug
// surfaced by the 2026-04-30 hvnode01 incident, where a long
// DELETE_SNAPSHOT and a live MIGRATE both reported "failed" in the
// SPA while libvirt happily completed both operations.
//
// What this resolves:
//   - 200 from /command-outcome → terminal: Update + emit
//     command.reconciled audit + EventCommand SSE.
//   - 404 from /command-outcome → agent doesn't know about this
//     request_id (pre-5.47.2 agent OR cache evicted). Bump
//     LastPolledAt; the entry's InflightUntil deadline still drives
//     eventual escalation to "unknown" via the stranded sweep.
//   - HTTP / network error → bump LastPolledAt; try again next tick.
//
// Phase 2 (PR-2a + onwards) replaces this push-poll loop with the
// agent's persistent ops.db + gRPC progress stream; until then this
// loop is the durable salvage path.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"kvm-center/internal/models"
)

const (
	// reconcilerInterval is how often the goroutine wakes to look
	// for entries to poll. Generous on purpose — the agent's outcome
	// cache holds answers for 35 m, so 30 s scans are plenty even on
	// a busy fleet.
	reconcilerInterval = 30 * time.Second
	// reconcilerPollInterval rate-limits per-entry polls. An entry
	// won't be polled more often than this.
	reconcilerPollInterval = 30 * time.Second
	// reconcilerHTTPTimeout is the per-request deadline when polling
	// the agent. Tight on purpose — /command-outcome is a cheap
	// in-memory map lookup.
	reconcilerHTTPTimeout = 5 * time.Second
	// reconcilerStrandedSweepInterval is how often we sweep entries
	// past their InflightUntil deadline to "unknown". The boot
	// reconciler runs once at startup; this keeps post-deadline
	// entries from sitting in pending limbo until the next restart.
	reconcilerStrandedSweepInterval = 5 * time.Minute
)

// reconcileHTTPDoer matches the subset of *http.Client used by the
// reconciler. Test injects a fake transport via this interface
// rather than spinning up an httptest.Server for every case.
type reconcileHTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// runCommandReconciler is the goroutine entrypoint. Returns when
// ctx is cancelled (server shutdown).
func runCommandReconciler(ctx context.Context, store *Store) {
	if store == nil || store.commandLog == nil {
		return
	}
	client := &http.Client{Timeout: reconcilerHTTPTimeout}
	pollTick := time.NewTicker(reconcilerInterval)
	defer pollTick.Stop()
	sweepTick := time.NewTicker(reconcilerStrandedSweepInterval)
	defer sweepTick.Stop()

	log.Printf("command-reconciler: started (interval=%s, http-timeout=%s)", reconcilerInterval, reconcilerHTTPTimeout)
	for {
		select {
		case <-ctx.Done():
			log.Printf("command-reconciler: stopping (%v)", ctx.Err())
			return
		case <-pollTick.C:
			reconcilerOnce(ctx, store, client)
		case <-sweepTick.C:
			reconcileStrandedCommands(store)
		}
	}
}

// reconcilerOnce performs a single pass: collect eligible entries,
// poll each agent, persist outcomes. Exposed for tests.
func reconcilerOnce(ctx context.Context, store *Store, client reconcileHTTPDoer) {
	now := time.Now()
	entries := store.commandLog.InflightForReconcile(now, reconcilerPollInterval)
	if len(entries) == 0 {
		return
	}
	for _, e := range entries {
		select {
		case <-ctx.Done():
			return
		default:
		}
		reconcileOne(ctx, store, client, e)
	}
}

// reconcileOne polls a single entry's owning agent and resolves the
// command-log row accordingly. Errors are absorbed (logged + LastPolledAt
// bumped); only a terminal answer transitions the entry.
func reconcileOne(ctx context.Context, store *Store, client reconcileHTTPDoer, e *models.CommandLogEntry) {
	addr := store.GetHostAgentAddr(e.HostID)
	if addr == "" {
		// Host has no agent IP — almost certainly removed from
		// inventory mid-flight. Mark unknown so the operator sees it.
		store.commandLog.Update(e.RequestID, "unknown", "host removed from inventory while command pending; outcome undetermined", 0, false)
		store.hub.Publish(EventCommand, eventPayload(e.RequestID, "unknown", e.HostID, e.Action))
		auditSystem(store, "command.reconciled", "vm/"+e.VMUUID, map[string]string{
			"request_id": e.RequestID,
			"action":     e.Action,
			"host_id":    e.HostID,
			"outcome":    "unknown",
			"reason":     "host_removed",
		}, true, "")
		notifyTaskOutcome(store, e, "unknown", "host removed from inventory while command pending")
		return
	}

	endpoint := fmt.Sprintf("http://%s/command-outcome?request_id=%s", addr, url.QueryEscape(e.RequestID))
	pollCtx, cancel := context.WithTimeout(ctx, reconcilerHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pollCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		// Constructed URL must always parse — but log defensively.
		log.Printf("command-reconciler: build req for %s failed: %v", e.RequestID, err)
		store.commandLog.RecordPoll(e.RequestID, time.Now())
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		// Network error — agent down, partition, etc. Stay pending,
		// try again next tick. Don't even log noise unless it's
		// something other than the usual "connection refused".
		if !errors.Is(err, context.Canceled) {
			log.Printf("command-reconciler: poll %s req_id=%s: %v", endpoint, e.RequestID, err)
		}
		store.commandLog.RecordPoll(e.RequestID, time.Now())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusNotFound:
		// Pre-5.47.2 agent OR cache evicted (we shouldn't see this
		// since cache TTL > our reconcile window, but defend).
		store.commandLog.RecordPoll(e.RequestID, time.Now())
		return
	case http.StatusOK:
		// fallthrough to parsing.
	default:
		// 4xx/5xx other than 404 — agent is reachable but unhappy.
		// Don't lock in a verdict; try again.
		log.Printf("command-reconciler: agent %s returned %d for req_id=%s: %s", addr, resp.StatusCode, e.RequestID, strings.TrimSpace(string(body)))
		store.commandLog.RecordPoll(e.RequestID, time.Now())
		return
	}

	var parsed models.CommandOutcomeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		log.Printf("command-reconciler: decode %s for req_id=%s: %v", endpoint, e.RequestID, err)
		store.commandLog.RecordPoll(e.RequestID, time.Now())
		return
	}
	switch parsed.Outcome {
	case "success", "failed":
		msg := parsed.Message
		if parsed.Outcome == "failed" && parsed.Error != "" {
			msg = parsed.Error
		}
		store.commandLog.Update(e.RequestID, parsed.Outcome, msg, parsed.StatusCode, false)
		store.hub.Publish(EventCommand, eventPayload(e.RequestID, parsed.Outcome, e.HostID, e.Action))
		auditSystem(store, "command.reconciled", "vm/"+e.VMUUID, map[string]string{
			"request_id": e.RequestID,
			"action":     e.Action,
			"host_id":    e.HostID,
			"outcome":    parsed.Outcome,
			"source":     "agent_outcome_cache",
		}, parsed.Outcome == "success", msg)
		// PR-3d: deliver outcome notification to operator channels.
		notifyTaskOutcome(store, e, parsed.Outcome, msg)
	default:
		// "running" / "unknown" — agent has a row but no terminal
		// answer yet. Stay pending, try again next tick.
		store.commandLog.RecordPoll(e.RequestID, time.Now())
	}
}

// eventPayload is the SSE shape for EventCommand. Kept small —
// clients refetch the row via GET /api/admin/command-log when they
// care about full detail.
func eventPayload(requestID, outcome, hostID, action string) map[string]any {
	return map[string]any{
		"request_id": requestID,
		"outcome":    outcome,
		"host_id":    hostID,
		"action":     action,
	}
}
