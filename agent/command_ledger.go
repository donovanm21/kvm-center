package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"kvm-center/internal/models"
)

var errCommandBinding = errors.New("request_id is bound to a different command")

// This ledger deliberately has no GC. Outcome detail retention must never
// turn an old request ID into permission to dispatch again. Legacy rows are
// imported without manufacturing proof that their workers returned.
func applyCommandLedger(ctx context.Context, db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS command_admissions (request_id TEXT PRIMARY KEY, action TEXT NOT NULL, vm_uuid TEXT NOT NULL, vm_name TEXT NOT NULL, params_digest TEXT NOT NULL, resolved_uuid TEXT NOT NULL DEFAULT '', resolved_name TEXT NOT NULL DEFAULT '', worker_returned INTEGER NOT NULL DEFAULT 0, cancellation_requested INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS command_target_fences (vm_uuid TEXT PRIMARY KEY, vm_name TEXT NOT NULL UNIQUE)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT request_id,action,COALESCE(vm_uuid,''),COALESCE(vm_name,''),COALESCE(params_json,'') FROM operations`)
	if err != nil {
		return err
	}
	type legacy struct{ id, action, uuid, name, digest string }
	var items []legacy
	for rows.Next() {
		var v legacy
		var raw string
		if err = rows.Scan(&v.id, &v.action, &v.uuid, &v.name, &raw); err != nil {
			rows.Close()
			return err
		}
		var params map[string]string
		if raw != "" {
			if err = json.Unmarshal([]byte(raw), &params); err != nil {
				rows.Close()
				return err
			}
		}
		v.digest = paramsDigest(params)
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		if _, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO command_admissions(request_id,action,vm_uuid,vm_name,params_digest) VALUES(?,?,?,?,?)`, v.id, strings.ToUpper(v.action), v.uuid, v.name, v.digest); err != nil {
			return err
		}
	}
	return nil
}

func paramsDigest(params map[string]string) string {
	if len(params) == 0 {
		params = nil
	}
	raw, _ := json.Marshal(params)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func sameCommand(req models.CommandRequest, action, uuid, name, params string) bool {
	return strings.ToUpper(req.Action) == strings.ToUpper(action) && req.VMUUID == uuid && req.VMName == name && paramsDigest(req.Params) == params

}

// admitCommand is serialized with target closure by a SQLite write transaction.
// A second agent opening this database cannot win the same request claim.
func (s *OperationsStore) admitCommand(ctx context.Context, req models.CommandRequest) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Take the writer reservation before reading either admission or fences.
	if _, err = tx.ExecContext(ctx, `UPDATE command_target_fences SET vm_name=vm_name WHERE 0`); err != nil {
		return false, err
	}
	var action, uuid, name, params string
	err = tx.QueryRowContext(ctx, `SELECT action,vm_uuid,vm_name,params_digest FROM command_admissions WHERE request_id=?`, req.RequestID).Scan(&action, &uuid, &name, &params)
	if err == nil {
		if !sameCommand(req, action, uuid, name, params) {
			return false, errCommandBinding
		}
		return false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if strings.ToUpper(req.Action) != "CLOSE_VM_COMMANDS" {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM command_target_fences WHERE lower(vm_uuid) IN (lower(?),lower(?)) OR vm_name IN (?,?)`, req.VMUUID, req.VMName, req.VMUUID, req.VMName).Scan(&count); err != nil {
			return false, err
		}
		if count != 0 {
			return false, errors.New("target command admission is permanently closed")
		}
	}
	// Rows can also be inserted by recovery/tests after schema initialization.
	var old Operation
	var oldParams sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT action,COALESCE(vm_uuid,''),COALESCE(vm_name,''),params_json FROM operations WHERE request_id=?`, req.RequestID).Scan(&old.Action, &old.VMUUID, &old.VMName, &oldParams)
	legacy := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if legacy {
		var params map[string]string
		if oldParams.String != "" {
			if err = json.Unmarshal([]byte(oldParams.String), &params); err != nil {
				return false, err
			}
		}
		if !sameCommand(req, old.Action, old.VMUUID, old.VMName, paramsDigest(params)) {
			return false, errCommandBinding
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO command_admissions(request_id,action,vm_uuid,vm_name,params_digest) VALUES(?,?,?,?,?)`, req.RequestID, strings.ToUpper(req.Action), req.VMUUID, req.VMName, paramsDigest(req.Params)); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return !legacy, nil
}

func (s *OperationsStore) workerReturned(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE command_admissions SET worker_returned=1 WHERE request_id=?`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("missing durable admission")
	}
	return nil
}

func (s *OperationsStore) requestCancellation(ctx context.Context, id string) error {
	// Legacy rows remain unresolved even after cancellation. No PID/status check
	// is allowed to fabricate the owning executor's return.
	op, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if op == nil {
		return errors.New("unknown request")
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO command_admissions(request_id,action,vm_uuid,vm_name,params_digest) VALUES(?,?,?,?,?)`, op.RequestID, strings.ToUpper(op.Action), op.VMUUID, op.VMName, paramsDigest(op.Params))
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, `UPDATE command_admissions SET cancellation_requested=1 WHERE request_id=?`, id)
	return err
}

func (s *OperationsStore) closeTarget(ctx context.Context, req models.CommandRequest) error {
	if req.VMUUID == "" || req.VMName == "" {
		return errors.New("closure requires exact UUID and name")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO command_target_fences(vm_uuid,vm_name) VALUES(?,?)`, req.VMUUID, req.VMName); err != nil {
		return err
	}
	var name string
	if err = tx.QueryRowContext(ctx, `SELECT vm_name FROM command_target_fences WHERE vm_uuid=?`, req.VMUUID).Scan(&name); err != nil {
		return err
	}
	if name != req.VMName {
		return errors.New("closed UUID/name binding mismatch")
	}
	return tx.Commit()
}

// DispatchEvidence describes command admission, NOT native/libvirt quiescence.
// Unsupported/legacy work keeps the target unresolved permanently; there is no
// reset/reopen endpoint. Other mutation routes require separate writer exclusion.
type DispatchEvidence struct {
	Durable                  bool   `json:"durable"`
	WorkerReturned           bool   `json:"worker_returned"`
	CancellationRequested    bool   `json:"cancellation_requested"`
	TargetAdmissionClosed    bool   `json:"target_admission_closed"`
	UnresolvedAdmissions     int    `json:"unresolved_admissions"`
	PowerDispatchDrained     bool   `json:"power_dispatch_drained"`
	NativeQuiescenceVerified bool   `json:"native_quiescence_verified"`
	Scope                    string `json:"scope"`
}

func (s *OperationsStore) dispatchEvidence(ctx context.Context, id string) (*DispatchEvidence, error) {
	var uuid, name string
	var returned, cancelled int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(resolved_uuid,''),vm_uuid),COALESCE(NULLIF(resolved_name,''),vm_name),worker_returned,cancellation_requested FROM command_admissions WHERE request_id=?`, id).Scan(&uuid, &name, &returned, &cancelled); err != nil {
		return nil, err
	}
	e := &DispatchEvidence{Durable: true, WorkerReturned: returned == 1, CancellationRequested: cancelled == 1, Scope: "/command synchronous power dispatch only; native state and other writers require independent verification"}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM command_target_fences WHERE vm_uuid=? AND vm_name=?`, uuid, name).Scan(&count); err != nil {
		return nil, err
	}
	e.TargetAdmissionClosed = count == 1
	if uuid == "" || name == "" {
		e.UnresolvedAdmissions = 1
		return e, nil
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM command_admissions WHERE (lower(vm_uuid) IN (lower(?),lower(?)) OR vm_name IN (?,?) OR lower(vm_name)=lower(?) OR lower(resolved_uuid)=lower(?) OR resolved_name=? OR (resolved_uuid='' AND worker_returned=0)) AND action<>'CLOSE_VM_COMMANDS' AND (worker_returned=0 OR action NOT IN ('START','STOP','FORCESTOP','RESTART'))`, uuid, name, uuid, name, uuid, uuid, name).Scan(&e.UnresolvedAdmissions); err != nil {
		return nil, err
	}
	e.PowerDispatchDrained = e.TargetAdmissionClosed && e.UnresolvedAdmissions == 0
	return e, nil
}

func (s *OperationsStore) ledgerClaim(ctx context.Context, req models.CommandRequest) (bool, error) {
	fresh, err := s.admitCommand(ctx, req)
	if err != nil || !fresh {
		return fresh, err
	}
	claimed, err := recordOpsLifecycleStart(req)
	if err != nil {
		return false, err
	}
	if !claimed {
		return false, fmt.Errorf("operation changed during durable admission")
	}
	return true, nil
}

// Resolve only a new owner. Submitted binding stays immutable for replay after
// native deletion; resolved identity is separately durable before native work.
func (s *OperationsStore) bindResolved(ctx context.Context, submitted, resolved models.CommandRequest) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE command_target_fences SET vm_name=vm_name WHERE 0`); err != nil {
		return err
	}
	if strings.ToUpper(submitted.Action) != "CLOSE_VM_COMMANDS" {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM command_target_fences WHERE lower(vm_uuid) IN (lower(?),lower(?)) OR vm_name IN (?,?)`, resolved.VMUUID, resolved.VMName, resolved.VMUUID, resolved.VMName).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("resolved target command admission is permanently closed")
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE command_admissions SET resolved_uuid=?,resolved_name=? WHERE request_id=?`, resolved.VMUUID, resolved.VMName, submitted.RequestID); err != nil {
		return err
	}
	return tx.Commit()
}
