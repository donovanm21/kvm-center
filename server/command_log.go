package main

// Durable command log for every mutating /api/command invocation.
// Enables:
//   - Idempotent retries: server mints/accepts a request_id, agent
//     returns cached outcome on replay (Tier S #4).
//   - Post-crash reconciliation: boot walks entries stuck in
//     "pending" and drives them to terminal state or marks
//     "unknown" for operator attention.
//   - Auditability: every mutation has one durable row with
//     actor + params + outcome + timing that outlives the
//     in-flight reply.
//
// Storage: on Mongo, the mcoCommandLog collection with a TTL on
// expires_at (set to SubmittedAt + commandLogRetentionDays when the
// entry goes terminal). On the JSON backend, a bounded in-memory
// slice capped at commandLogSliceCap — enough for reconciliation
// across a crash, not a substitute for Mongo in production.

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"kvm-center/internal/models"
)

const (
	commandLogRetentionDays = 30
	commandLogSliceCap      = 5000
	// pendingReconcileAge is how long a "pending" entry must sit
	// before the boot reconciler gives up and marks it "unknown".
	// Longer than the longest realistic synchronous command (a
	// storage-copy migration can run 30+ minutes), shorter than a
	// full day.
	pendingReconcileAge = 45 * time.Minute
)

// NewRequestID returns a 128-bit random token encoded as 32 hex
// chars. Not UUID-formatted because we don't need interop with any
// external system — just collision resistance.
func NewRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "req_" + hex.EncodeToString(b[:])
}

// CommandLog is the in-memory index of recent CommandLogEntry
// records. The Mongo backend writes through to disk on every
// Record/Update call; the JSON backend relies on the periodic
// store.Save() tick like every other mutable collection.
type CommandLog struct {
	mu      sync.Mutex
	entries map[string]*models.CommandLogEntry
}

func NewCommandLog() *CommandLog {
	return &CommandLog{entries: make(map[string]*models.CommandLogEntry)}
}

// Record inserts a new pending entry. If an entry with the same
// request_id already exists it is returned unchanged AND the second
// return is true — caller uses this to short-circuit replay at the
// server boundary without even calling the agent.
func (l *CommandLog) Record(entry *models.CommandLogEntry) (*models.CommandLogEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.entries[entry.RequestID]; ok {
		return existing, true
	}
	cp := *entry
	l.entries[entry.RequestID] = &cp
	l.enforceCap()
	return &cp, false
}

// Update records the outcome for an in-flight entry. No-ops when the
// request_id is unknown (shouldn't happen in the normal flow but
// cheap to defend against).
func (l *CommandLog) Update(requestID, outcome, message string, statusCode int, replayed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[requestID]
	if !ok {
		return
	}
	now := time.Now()
	e.AppliedAt = &now
	e.Outcome = outcome
	e.Message = message
	e.StatusCode = statusCode
	e.Replayed = replayed
	if outcome != "pending" {
		exp := now.Add(commandLogRetentionDays * 24 * time.Hour)
		e.ExpiresAt = &exp
	}
}

// Pending returns the entries still in-flight older than minAge.
// Boot reconciler iterates this set to resolve stale records.
func (l *CommandLog) Pending(minAge time.Duration) []*models.CommandLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-minAge)
	var out []*models.CommandLogEntry
	for _, e := range l.entries {
		if e.Outcome == "pending" && e.SubmittedAt.Before(cutoff) {
			cp := *e
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt.Before(out[j].SubmittedAt) })
	return out
}

// List returns the most recent entries, newest first, capped at
// `limit` (0 = no cap). Used for the admin UI + debug endpoints.
func (l *CommandLog) List(limit int) []*models.CommandLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*models.CommandLogEntry, 0, len(l.entries))
	for _, e := range l.entries {
		cp := *e
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt.After(out[j].SubmittedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// snapshot returns the current entries for persistence. Called by
// the backend Save path.
func (l *CommandLog) snapshot() map[string]*models.CommandLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]*models.CommandLogEntry, len(l.entries))
	for k, v := range l.entries {
		cp := *v
		out[k] = &cp
	}
	return out
}

// replace swaps the in-memory set, typically called from Load.
func (l *CommandLog) replace(m map[string]*models.CommandLogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = make(map[string]*models.CommandLogEntry, len(m))
	for k, v := range m {
		cp := *v
		l.entries[k] = &cp
	}
	l.enforceCap()
}

// reconcileStrandedCommands walks the command log at boot and flips
// any "pending" entry older than pendingReconcileAge to "unknown".
// We deliberately do NOT retry — we don't know whether the agent
// already applied the command (lost response) or didn't see it at
// all (dropped connection). Either outcome might be correct; the
// operator has to look and decide. The admin UI surfaces them with
// enough context (actor, action, target, submitted_at) to resolve
// manually.
func reconcileStrandedCommands(store *Store) {
	if store == nil || store.commandLog == nil {
		return
	}
	pending := store.commandLog.Pending(pendingReconcileAge)
	if len(pending) == 0 {
		return
	}
	log.Printf("command-reconcile: found %d stranded command entries, marking unknown", len(pending))
	for _, e := range pending {
		store.commandLog.Update(e.RequestID, "unknown", "server restarted while command was in flight; outcome undetermined — check the target VM state and resolve manually", 0, false)
		auditSystem(store, "command.stranded", "vm/"+e.VMUUID, map[string]string{
			"request_id": e.RequestID,
			"action":     e.Action,
			"host_id":    e.HostID,
			"vm_name":    e.VMName,
		}, false, "stranded by server restart")
	}
}

// handleCommandLog exposes the command log to the admin UI. Admin-
// gated (user.manage). Query params:
//
//	?limit=N       — cap results at N (default 200)
//	?outcome=...   — filter by outcome (pending, success, failed, unknown)
func handleCommandLog(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		limit := 200
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
				limit = n
			}
		}
		outcome := r.URL.Query().Get("outcome")
		entries := store.commandLog.List(0) // filter then cap below
		if outcome != "" {
			filt := entries[:0]
			for _, e := range entries {
				if e.Outcome == outcome {
					filt = append(filt, e)
				}
			}
			entries = filt
		}
		if len(entries) > limit {
			entries = entries[:limit]
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"entries": entries,
			"count":   len(entries),
		})
	}
}

// enforceCap drops the oldest TERMINAL entries until len <= cap.
// Pending entries always stick around so the reconciler can find
// them. Must be called with l.mu held.
func (l *CommandLog) enforceCap() {
	if len(l.entries) <= commandLogSliceCap {
		return
	}
	type kv struct {
		id string
		e  *models.CommandLogEntry
	}
	terminals := make([]kv, 0, len(l.entries))
	for k, v := range l.entries {
		if v.Outcome != "pending" {
			terminals = append(terminals, kv{k, v})
		}
	}
	sort.Slice(terminals, func(i, j int) bool {
		return terminals[i].e.SubmittedAt.Before(terminals[j].e.SubmittedAt)
	})
	excess := len(l.entries) - commandLogSliceCap
	for i := 0; i < excess && i < len(terminals); i++ {
		delete(l.entries, terminals[i].id)
	}
}
