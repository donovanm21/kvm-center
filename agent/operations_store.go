package main

// Persistent operations store backing the agent's bullet-proof
// command lifecycle (v5.48+).
//
// Replaces the in-memory outcomeCache from v5.32.17 with a SQLite
// database on disk so:
//   - command outcomes survive an agent restart (the cache window
//     doesn't matter; rows live until GC),
//   - the boot reconciler (PR-2d) can scan running-state rows on
//     startup and resolve them against libvirt reality,
//   - the Recent Tasks UX (Phase 3) can show a forensic history
//     beyond the 35-min cache window.
//
// Path: /var/lib/kvm-center/operations.db (overrideable via the
// KVM_AGENT_OPS_DB env var for tests). Pure-Go SQLite via
// modernc.org/sqlite — no CGO so the agent binary stays portable.
//
// Concurrency: SQLite WAL + NORMAL sync handles concurrent reads
// while one writer holds a transaction. We rely on database/sql's
// connection pool rather than rolling our own mutex.
//
// Retention: terminal entries (success/failed/cancelled/unknown)
// are kept 7 days for forensics, then GCed. Running/pending
// entries are kept indefinitely until they reach a terminal state
// — a stuck row is data, not noise.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kvm-center/internal/models"

	_ "modernc.org/sqlite"
)

const (
	// DefaultOpsDBPath is the canonical on-disk location. Override
	// via the KVM_AGENT_OPS_DB env var (used by tests).
	DefaultOpsDBPath = "/var/lib/kvm-center/operations.db"

	// OpsRetentionTerminal is how long terminal rows are kept before
	// GC. Long enough to investigate "what happened on host-a last
	// Friday" without bloating the file. ~100 KB across a busy
	// fleet-week.
	OpsRetentionTerminal = 7 * 24 * time.Hour

	// opsDBOpenTimeout is the deadline for the initial open + schema
	// migration. Long enough for a slow-disk fsync but bounded so a
	// stuck filesystem doesn't deadlock the agent at startup.
	opsDBOpenTimeout = 10 * time.Second
)

// Operation states. Mirrors the spec §1 schema enum.
const (
	OpStatePending   = "pending"
	OpStateRunning   = "running"
	OpStateSuccess   = "success"
	OpStateFailed    = "failed"
	OpStateCancelled = "cancelled"
	OpStateUnknown   = "unknown"
)

// IsTerminalOpState returns true if the state is one we won't
// transition out of.
func IsTerminalOpState(s string) bool {
	switch s {
	case OpStateSuccess, OpStateFailed, OpStateCancelled, OpStateUnknown:
		return true
	}
	return false
}

// Operation is a single row in the operations table. Mirrors the
// columns in spec §1; pointer fields are nullable in SQLite so
// callers can distinguish "not set yet" from "zero value".
type Operation struct {
	RequestID       string            `json:"request_id"`
	Action          string            `json:"action"`
	VMUUID          string            `json:"vm_uuid,omitempty"`
	VMName          string            `json:"vm_name,omitempty"`
	Params          map[string]string `json:"params,omitempty"`
	State           string            `json:"state"`
	SubmittedAt     time.Time         `json:"submitted_at"`
	StartedAt       *time.Time        `json:"started_at,omitempty"`
	LastHeartbeatAt *time.Time        `json:"last_heartbeat_at,omitempty"`
	CompletedAt     *time.Time        `json:"completed_at,omitempty"`
	Output          string            `json:"output,omitempty"`
	Error           string            `json:"error,omitempty"`
	ChildPID        int               `json:"child_pid,omitempty"`
	WatchdogAt      *time.Time        `json:"watchdog_at,omitempty"`
}

// ops is the package-global handle. nil when ops.db couldn't be
// opened — callers must guard with `if ops != nil` until PR-2b
// makes the substrate mandatory.
var ops *OperationsStore

// OperationsStore wraps the SQLite database. One instance per
// agent process; mounted on the package-global `ops` after open.
type OperationsStore struct {
	db   *sql.DB
	path string
}

// OpenOperationsStore opens (or creates) the SQLite DB at path,
// applies the schema if missing, and returns a ready-to-use store.
//
// On a corruption-detected open we rename the bad file aside and
// rebuild from scratch. We deliberately lose the in-flight history
// in that case rather than refusing to start — libvirt is the
// ultimate source of truth, the boot reconciler will recover state
// from it on PR-2d, and a non-starting agent is much worse than a
// blank ops.db.
func OpenOperationsStore(path string) (*OperationsStore, error) {
	if path == "" {
		path = DefaultOpsDBPath
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("ops.db: create parent dir: %w", err)
	}
	store, err := openOpsDB(path)
	if err == nil {
		return store, nil
	}
	if !isCorruption(err) {
		return nil, err
	}
	log.Printf("ops.db: corruption detected at %s (%v); renaming aside and rebuilding", path, err)
	if mvErr := renameCorrupted(path); mvErr != nil {
		return nil, fmt.Errorf("ops.db: rename corrupted file: %w", mvErr)
	}
	return openOpsDB(path)
}

func openOpsDB(path string) (*OperationsStore, error) {
	// pragma=journal_mode=WAL via DSN doesn't always stick on the
	// modernc driver; we exec the PRAGMAs manually after open.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("ops.db: sql.Open: %w", err)
	}
	// modernc.org/sqlite is single-connection-friendly but our usage
	// is light; cap the pool to avoid surprise lock contention.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	ctx, cancel := context.WithTimeout(context.Background(), opsDBOpenTimeout)
	defer cancel()
	for _, p := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := db.ExecContext(ctx, p); err != nil {
			db.Close()
			return nil, fmt.Errorf("ops.db: pragma %q: %w", p, err)
		}
	}
	if err := applyOpsSchema(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("ops.db: schema: %w", err)
	}
	// Quick integrity probe — surfaces corruption that schema
	// creation didn't trigger (e.g. damaged data pages).
	var ok string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&ok); err != nil {
		db.Close()
		return nil, fmt.Errorf("ops.db: integrity_check: %w", err)
	}
	if strings.ToLower(strings.TrimSpace(ok)) != "ok" {
		db.Close()
		return nil, fmt.Errorf("ops.db: integrity_check failed: %s", ok)
	}
	return &OperationsStore{db: db, path: path}, nil
}

func applyOpsSchema(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS operations (
			request_id        TEXT PRIMARY KEY,
			action            TEXT NOT NULL,
			vm_uuid           TEXT,
			vm_name           TEXT,
			params_json       TEXT,
			state             TEXT NOT NULL,
			submitted_at      INTEGER NOT NULL,
			started_at        INTEGER,
			last_heartbeat_at INTEGER,
			completed_at      INTEGER,
			output            TEXT,
			error             TEXT,
			child_pid         INTEGER,
			watchdog_at       INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS ops_state ON operations(state)`,
		`CREATE INDEX IF NOT EXISTS ops_heartbeat ON operations(last_heartbeat_at)`,
		`CREATE INDEX IF NOT EXISTS ops_completed ON operations(completed_at)`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// Close shuts down the underlying database. Only used at agent
// shutdown / in tests; production callers leave it open.
func (s *OperationsStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Insert records a new operation row. Idempotent on request_id —
// the second insert with the same id is a no-op so the caller
// doesn't have to guard against a retried HTTP /command.
func (s *OperationsStore) Insert(ctx context.Context, op Operation) error {
	if op.RequestID == "" {
		return errors.New("ops.db: Insert with empty request_id")
	}
	if op.SubmittedAt.IsZero() {
		op.SubmittedAt = time.Now()
	}
	params := ""
	if len(op.Params) > 0 {
		b, err := json.Marshal(op.Params)
		if err != nil {
			return fmt.Errorf("ops.db: marshal params: %w", err)
		}
		params = string(b)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO operations (
			request_id, action, vm_uuid, vm_name, params_json,
			state, submitted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		op.RequestID, op.Action, op.VMUUID, op.VMName, params,
		op.State, op.SubmittedAt.UnixNano())
	return err
}

// UpdateStateOpts is the diff applied by UpdateState. Zero-valued
// fields are not written.
type UpdateStateOpts struct {
	State       string
	Output      string
	Error       string
	ChildPID    int
	StartedAt   *time.Time
	CompletedAt *time.Time
	WatchdogAt  *time.Time
	// ClearChildPID, when true, sets child_pid back to NULL even if
	// ChildPID is zero. Used when the watchdog or a successful exit
	// reaps the child and we want to drop the tracker.
	ClearChildPID bool
}

// UpdateState applies a partial update to an existing row.
// Returns sql.ErrNoRows if the request_id is unknown.
func (s *OperationsStore) UpdateState(ctx context.Context, requestID string, opts UpdateStateOpts) error {
	if requestID == "" {
		return errors.New("ops.db: UpdateState with empty request_id")
	}
	sets := []string{}
	args := []any{}
	if opts.State != "" {
		sets = append(sets, "state = ?")
		args = append(args, opts.State)
	}
	if opts.Output != "" {
		sets = append(sets, "output = ?")
		args = append(args, opts.Output)
	}
	if opts.Error != "" {
		sets = append(sets, "error = ?")
		args = append(args, opts.Error)
	}
	if opts.ChildPID > 0 {
		sets = append(sets, "child_pid = ?")
		args = append(args, opts.ChildPID)
	} else if opts.ClearChildPID {
		sets = append(sets, "child_pid = NULL")
	}
	if opts.StartedAt != nil {
		sets = append(sets, "started_at = ?")
		args = append(args, opts.StartedAt.UnixNano())
	}
	if opts.CompletedAt != nil {
		sets = append(sets, "completed_at = ?")
		args = append(args, opts.CompletedAt.UnixNano())
	}
	if opts.WatchdogAt != nil {
		sets = append(sets, "watchdog_at = ?")
		args = append(args, opts.WatchdogAt.UnixNano())
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, requestID)
	res, err := s.db.ExecContext(ctx,
		"UPDATE operations SET "+strings.Join(sets, ", ")+" WHERE request_id = ?", args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RecordHeartbeat stamps last_heartbeat_at = now on the row. Cheap
// — called every 30 s per running operation. No-op if the row is
// already terminal (a heartbeat after success is a bug elsewhere
// we don't want to mask, but we also don't want to corrupt the row).
func (s *OperationsStore) RecordHeartbeat(ctx context.Context, requestID string, at time.Time) error {
	if requestID == "" {
		return errors.New("ops.db: RecordHeartbeat with empty request_id")
	}
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE operations
		 SET last_heartbeat_at = ?
		 WHERE request_id = ? AND state IN (?, ?)`,
		at.UnixNano(), requestID, OpStatePending, OpStateRunning)
	return err
}

// Get returns the row for requestID, or nil if not found.
func (s *OperationsStore) Get(ctx context.Context, requestID string) (*Operation, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT request_id, action, vm_uuid, vm_name, params_json,
		       state, submitted_at, started_at, last_heartbeat_at,
		       completed_at, output, error, child_pid, watchdog_at
		FROM operations WHERE request_id = ?`, requestID)
	op, err := scanOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return op, err
}

// ListByState returns all rows matching state, oldest first.
// Useful for boot reconciliation (states "running" + "pending")
// and admin listings.
func (s *OperationsStore) ListByState(ctx context.Context, state string) ([]*Operation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT request_id, action, vm_uuid, vm_name, params_json,
		       state, submitted_at, started_at, last_heartbeat_at,
		       completed_at, output, error, child_pid, watchdog_at
		FROM operations WHERE state = ? ORDER BY submitted_at ASC`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Operation
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// GC deletes terminal rows whose completed_at is older than
// retention. Returns the number of rows pruned. Safe to run while
// other writers are active — SQLite handles concurrency under WAL.
func (s *OperationsStore) GC(ctx context.Context, retention time.Duration) (int, error) {
	cutoff := time.Now().Add(-retention).UnixNano()
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM operations
		WHERE state IN (?, ?, ?, ?)
		  AND completed_at IS NOT NULL
		  AND completed_at < ?`,
		OpStateSuccess, OpStateFailed, OpStateCancelled, OpStateUnknown,
		cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// scanOperation accepts either *sql.Row or *sql.Rows so callers
// can share parsing.
type scanner interface {
	Scan(dest ...any) error
}

func scanOperation(s scanner) (*Operation, error) {
	var (
		op             Operation
		vmUUID, vmName sql.NullString
		params         sql.NullString
		started        sql.NullInt64
		heartbeat      sql.NullInt64
		completed      sql.NullInt64
		output, errStr sql.NullString
		childPID       sql.NullInt64
		watchdog       sql.NullInt64
		submittedNs    int64
	)
	if err := s.Scan(
		&op.RequestID, &op.Action, &vmUUID, &vmName, &params,
		&op.State, &submittedNs, &started, &heartbeat,
		&completed, &output, &errStr, &childPID, &watchdog,
	); err != nil {
		return nil, err
	}
	op.VMUUID = vmUUID.String
	op.VMName = vmName.String
	if params.Valid && params.String != "" {
		_ = json.Unmarshal([]byte(params.String), &op.Params)
	}
	op.SubmittedAt = time.Unix(0, submittedNs)
	if started.Valid {
		t := time.Unix(0, started.Int64)
		op.StartedAt = &t
	}
	if heartbeat.Valid {
		t := time.Unix(0, heartbeat.Int64)
		op.LastHeartbeatAt = &t
	}
	if completed.Valid {
		t := time.Unix(0, completed.Int64)
		op.CompletedAt = &t
	}
	op.Output = output.String
	op.Error = errStr.String
	if childPID.Valid {
		op.ChildPID = int(childPID.Int64)
	}
	if watchdog.Valid {
		t := time.Unix(0, watchdog.Int64)
		op.WatchdogAt = &t
	}
	return &op, nil
}

// opsLookupDeadline bounds synchronous reads from the ops store
// (e.g. the outcome-cache read-through) so a stuck disk doesn't
// block the HTTP handler. Used as a per-call context.WithTimeout.
const opsLookupDeadline = 2 * time.Second

// synthesizeOutcomeFromOp reconstructs the (status, body, cachedAt)
// shape that the v5.32.17 in-memory outcome cache held — so the
// /command-outcome handler and any other consumer of
// outcomes.lookup() can treat ops.db as a transparent read-through.
//
// Output: a JSON body `{success, message, error}` that matches what
// the agent's command handler returns on the first execution.
func synthesizeOutcomeFromOp(op *Operation) (status int, body []byte, cachedAt time.Time, ok bool) {
	if op == nil {
		return 0, nil, time.Time{}, false
	}
	switch op.State {
	case OpStateSuccess:
		status = 200
	case OpStateFailed, OpStateCancelled, OpStateUnknown:
		status = 500
	default:
		return 0, nil, time.Time{}, false
	}
	resp := struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error,omitempty"`
	}{
		Success: op.State == OpStateSuccess,
		Message: op.Output,
		Error:   op.Error,
	}
	if op.State == OpStateSuccess && resp.Message == "" {
		resp.Message = "ok"
	}
	if op.State != OpStateSuccess && resp.Error == "" {
		resp.Error = op.Output
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return 0, nil, time.Time{}, false
	}
	at := op.SubmittedAt
	if op.CompletedAt != nil {
		at = *op.CompletedAt
	}
	return status, b, at, true
}

// recordOpsLifecycleStart inserts a "running" row in ops.db at the
// start of every command (v5.48 PR-2b). Best-effort: a failure here
// shouldn't block the command — degraded durability is preferable
// to refusing operator actions.
//
// Each command gets one row keyed on request_id. Insert is
// idempotent so a retry against a brand-new agent (cache lost,
// db row already exists) is a no-op.
func recordOpsLifecycleStart(req models.CommandRequest) {
	if ops == nil || req.RequestID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), opsLookupDeadline)
	defer cancel()
	now := time.Now()
	op := Operation{
		RequestID:   req.RequestID,
		Action:      strings.ToUpper(req.Action),
		VMUUID:      req.VMUUID,
		VMName:      req.VMName,
		Params:      req.Params,
		State:       OpStateRunning,
		SubmittedAt: now,
	}
	if err := ops.Insert(ctx, op); err != nil {
		log.Printf("ops.db: insert lifecycle row req_id=%s action=%s: %v", req.RequestID, req.Action, err)
		return
	}
	if err := ops.UpdateState(ctx, req.RequestID, UpdateStateOpts{
		State:     OpStateRunning,
		StartedAt: &now,
	}); err != nil {
		// Insert succeeded but the second update failed — log and
		// move on. The row is at least visible.
		log.Printf("ops.db: stamp started_at req_id=%s: %v", req.RequestID, err)
	}
}

// recordOpsLifecycleEnd is the deferred counterpart to
// recordOpsLifecycleStart — runs after the action body returns,
// derives a terminal state from the HTTP status + body, and
// records it in ops.db. Best-effort: silent on success, logged on
// failure, never blocks the deferred response cache write.
//
// Status code → state mapping:
//   - 2xx → OpStateSuccess
//   - 4xx/5xx → OpStateFailed
//
// PR-2f guard: if the row is already in a terminal state when we
// arrive here (e.g. the operator cancelled it while the goroutine
// was still running), we leave the cancelled outcome in place and
// don't overwrite. Same goes for any race where another writer
// got there first.
func recordOpsLifecycleEnd(requestID string, status int, body []byte) {
	if ops == nil || requestID == "" {
		return
	}
	state := OpStateSuccess
	if status >= 400 {
		state = OpStateFailed
	}
	ctx, cancel := context.WithTimeout(context.Background(), opsLookupDeadline)
	defer cancel()
	if cur, err := ops.Get(ctx, requestID); err == nil && cur != nil && IsTerminalOpState(cur.State) {
		log.Printf("ops.db: req_id=%s already %s; not overwriting with %s", requestID, cur.State, state)
		return
	}
	now := time.Now()
	output, errMsg := parseLifecycleBody(body, state)
	opts := UpdateStateOpts{
		State:         state,
		Output:        output,
		Error:         errMsg,
		ClearChildPID: true,
		CompletedAt:   &now,
	}
	if err := ops.UpdateState(ctx, requestID, opts); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("ops.db: update terminal state req_id=%s: %v", requestID, err)
		}
	}
}

// parseLifecycleBody extracts the message + error fields from the
// agent's standard CommandResponse JSON body. Falls back to the
// raw body when JSON parsing fails so the row still has something
// useful for forensics.
func parseLifecycleBody(body []byte, state string) (output, errMsg string) {
	var parsed struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		// Non-JSON response (curl, malformed). Stash the raw bytes.
		raw := strings.TrimSpace(string(body))
		if state == OpStateSuccess {
			return raw, ""
		}
		return "", raw
	}
	if state == OpStateSuccess {
		return parsed.Message, ""
	}
	if parsed.Error != "" {
		return parsed.Message, parsed.Error
	}
	return parsed.Message, parsed.Message
}

// runOpsGC is the periodic GC goroutine started from agent main.
// Single tick is exposed (gcOnce) for tests.
func runOpsGC(ctx context.Context, store *OperationsStore) {
	if store == nil {
		return
	}
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if n, err := store.GC(ctx, OpsRetentionTerminal); err != nil {
				log.Printf("ops.db: GC error: %v", err)
			} else if n > 0 {
				log.Printf("ops.db: GC pruned %d terminal rows", n)
			}
		}
	}
}

// isCorruption returns true for SQLite errors that justify
// renaming the file aside and rebuilding. We deliberately keep the
// list narrow — a permission error or full-disk shouldn't trigger
// a rebuild.
func isCorruption(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "malformed") ||
		strings.Contains(msg, "not a database") ||
		strings.Contains(msg, "file is encrypted or is not a database") ||
		strings.Contains(msg, "integrity_check failed")
}

// renameCorrupted moves path → path.corrupt-<unix>.db so the
// operator can later inspect it without the agent re-using a bad
// file forever.
func renameCorrupted(path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	bak := fmt.Sprintf("%s.corrupt-%d.db", path, time.Now().Unix())
	if err := os.Rename(path, bak); err != nil {
		return err
	}
	// WAL + SHM sidecars get cleared too — leaving them confuses
	// the rebuilt DB.
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	log.Printf("ops.db: corrupt file moved to %s", bak)
	return nil
}
