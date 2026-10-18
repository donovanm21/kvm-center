// Package nativework supplies a durable registered-writer contract. It is not a
// native quiescence producer: production writer coverage and qualified native
// adapters are deliberately not installed by this package.
package nativework

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const maxRecords = 4096

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,255}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var ErrReplay = errors.New("request permanently bound; no new execution permission")
var ErrBlocked = errors.New("writer admission closed or unresolved")
var ErrUncertain = errors.New("journal durability uncertain; this handle is fenced")

type Target struct{ UUID, Name string }
type Request struct {
	ID, AttemptID, Action, ParamsSHA256 string
	Target                              Target
}

// Identity is immutable and supplied by the journal to the adapter, never copied
// from a terminal command response. Session identifies the live lease owner.
type Identity struct {
	Request                     Request
	WriterID, ParentID, Session string
}

// StopObserver is a trusted adapter capability bound before permission to start.
// AwaitStopped must positively observe cessation of this exact writer's native
// effects. It must return an error for unknown/restarted handles, unsupported
// work, cancellation requests, timeouts, or mere command/process return without
// native cessation. The package includes no production implementation.
type StopObserver interface {
	AwaitStopped(context.Context, Identity) error
}

// Lease cannot be reconstructed from persistence or transferred between handles.
type Lease struct {
	journal  *Journal
	identity Identity
	observer StopObserver
}

func (l *Lease) Identity() Identity {
	if l == nil {
		return Identity{}
	}
	return l.identity
}

type Journal struct {
	db                *sql.DB
	session           string
	mu                sync.Mutex
	closed, uncertain bool
	// Tests inject acknowledgement loss AFTER the actual commit.
	afterCommit func() error
}

type Evidence struct {
	Target                        Target `json:"target"`
	Revision                      int64  `json:"revision"`
	TargetAdmissionClosed         bool   `json:"target_admission_closed"`
	RegisteredWriters             int    `json:"registered_writers"`
	RegisteredWritersUnresolved   int    `json:"registered_writers_unresolved"`
	RegisteredWriterDrainVerified bool   `json:"registered_writer_drain_verified"`
	NativeQuiescenceVerified      bool   `json:"native_quiescence_verified"`
	WriterCoverageComplete        bool   `json:"writer_coverage_complete"`
	Scope                         string `json:"scope"`
}

func textOK(s string) bool {
	return s != "" && len(s) <= 256 && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func validTarget(t Target) bool {
	return uuidPattern.MatchString(t.UUID) && namePattern.MatchString(t.Name)
}
func validRequest(r Request) bool {
	return textOK(r.ID) && textOK(r.AttemptID) && textOK(r.Action) && r.Action == strings.ToUpper(r.Action) && digestPattern.MatchString(r.ParamsSHA256) && validTarget(r.Target)
}

// Open uses a dedicated SQLite journal. It never imports legacy operations or
// replaces an old owner. Unresolved records survive indefinitely. Existing files
// with another schema/version are refused; originals/backups must not be deleted
// or rolled back to manufacture permission. Concurrent handles serialize writes.
func Open(path string) (*Journal, error) {
	if path == "" || strings.ContainsAny(path, "?\x00") || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, errors.New("dedicated filesystem journal path required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var tx *sql.Tx
	fail := func(e error) (*Journal, error) {
		if tx != nil {
			_ = tx.Rollback()
		}
		_ = db.Close()
		return nil, e
	}
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA synchronous=FULL"} {
		if _, err = db.Exec(q); err != nil {
			return fail(err)
		}
	}
	tx, err = db.Begin()
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT GLOB 'sqlite_*'`).Scan(&count); err != nil {
		return fail(err)
	}
	if count == 0 {
		for _, name := range schemaOrder {
			if _, err = tx.Exec(supportedSchema[name]); err != nil {
				return fail(err)
			}
		}
		if _, err = tx.Exec(`INSERT INTO native_meta VALUES(1)`); err != nil {
			return fail(err)
		}
	}
	if err = validateJournal(context.Background(), tx); err != nil {
		return fail(err)
	}

	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return fail(err)
	}
	return &Journal{db: db, session: hex.EncodeToString(token)}, nil
}
func (j *Journal) usable() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return errors.New("journal closed")
	}
	if j.uncertain {
		return ErrUncertain
	}
	return nil
}
func (j *Journal) Close() error { j.mu.Lock(); j.closed = true; j.mu.Unlock(); return j.db.Close() }
func (j *Journal) commit(tx *sql.Tx) error {
	err := tx.Commit()
	if err == nil && j.afterCommit != nil {
		err = j.afterCommit()
	}
	if err != nil {
		j.mu.Lock()
		j.uncertain = true
		j.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	return nil
}
func (j *Journal) write(ctx context.Context) (*sql.Tx, error) {
	if err := j.usable(); err != nil {
		return nil, err
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Acquire SQLite's writer reservation BEFORE reading admission state.
	if _, err = tx.ExecContext(ctx, `UPDATE native_meta SET version=version`); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err = validateJournal(ctx, tx); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err = j.usable(); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func targetRow(ctx context.Context, tx *sql.Tx, t Target, create bool) (bool, error) {
	if !validTarget(t) {
		return false, errors.New("invalid exact target identity")
	}
	rows, err := tx.QueryContext(ctx, `SELECT uuid,name,closed FROM native_targets WHERE uuid=? OR name=? COLLATE NOCASE OR uuid=lower(?) OR name=? COLLATE NOCASE`, t.UUID, t.Name, t.Name, t.UUID)
	if err != nil {
		return false, err
	}
	found, closed := false, false
	for rows.Next() {
		var u, n string
		var c int
		if err = rows.Scan(&u, &n, &c); err != nil {
			break
		}
		if found || u != t.UUID || n != t.Name {
			err = errors.New("conflicting target identity or alias")
			break
		}
		found = true
		closed = c == 1
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	if !found {
		if !create {
			return false, errors.New("unknown target")
		}
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_targets`).Scan(&n); err != nil {
			return false, err
		}
		if n >= maxRecords {
			return false, errors.New("target ledger capacity reached; retained fences cannot be discarded")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO native_targets(uuid,name) VALUES(?,?)`, t.UUID, t.Name)
	}
	return closed, err
}
func capacity(ctx context.Context, tx *sql.Tx) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_writers`).Scan(&n); err != nil {
		return err
	}
	if n >= maxRecords {
		return errors.New("writer ledger capacity reached; records must be retained")
	}
	return nil
}
func revise(ctx context.Context, tx *sql.Tx, t Target) error {
	_, err := tx.ExecContext(ctx, `UPDATE native_targets SET revision=revision+1 WHERE uuid=?`, t.UUID)
	return err
}

// Begin registers a root writer durably before returning permission to start.
// An identical retry is NOT a lease. A new attempt/request is blocked while any
// writer for the exact target remains unresolved, even after logical completion.
func (j *Journal) Begin(ctx context.Context, r Request, observer StopObserver) (*Lease, error) {
	if !validRequest(r) || observer == nil {
		return nil, errors.New("valid request and bound stop observer required")
	}
	tx, err := j.write(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var old Request
	old.Target = r.Target
	var target, name string
	err = tx.QueryRowContext(ctx, `SELECT attempt,action,digest,target,name FROM native_requests WHERE id=?`, r.ID).Scan(&old.AttemptID, &old.Action, &old.ParamsSHA256, &target, &name)
	if err == nil {
		old.ID = r.ID
		old.Target = Target{target, name}
		if old != r {
			return nil, errors.New("request identity reused with different binding")
		}
		return nil, ErrReplay
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	closed, err := targetRow(ctx, tx, r.Target, true)
	if err != nil {
		return nil, err
	}
	if closed {
		return nil, ErrBlocked
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_writers w JOIN native_requests r ON r.id=w.request WHERE r.target=? AND w.stopped=0`, r.Target.UUID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending != 0 {
		return nil, ErrBlocked
	}
	if err = capacity(ctx, tx); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_requests(id,attempt,action,digest,target,name,session) VALUES(?,?,?,?,?,?,?)`, r.ID, r.AttemptID, r.Action, r.ParamsSHA256, r.Target.UUID, r.Target.Name, j.session); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_writers(request,writer,parent) VALUES(?,'root','')`, r.ID); err != nil {
		return nil, err
	}
	if err = revise(ctx, tx, r.Target); err != nil {
		return nil, err
	}
	if err = j.commit(tx); err != nil {
		return nil, err
	}
	return &Lease{j, Identity{r, "root", "", j.session}, observer}, nil
}
func (j *Journal) owner(ctx context.Context, tx *sql.Tx, l *Lease) (bool, error) {
	if l == nil || l.journal != j || l.identity.Session != j.session || l.observer == nil {
		return false, errors.New("foreign or recovered lease")
	}
	i := l.identity
	var attempt, action, digest, target, name, session, parent string
	var stopped int
	err := tx.QueryRowContext(ctx, `SELECT r.attempt,r.action,r.digest,r.target,r.name,r.session,w.parent,w.stopped FROM native_requests r JOIN native_writers w ON w.request=r.id WHERE r.id=? AND w.writer=?`, i.Request.ID, i.WriterID).Scan(&attempt, &action, &digest, &target, &name, &session, &parent, &stopped)
	if err != nil {
		return false, err
	}
	if attempt != i.Request.AttemptID || action != i.Request.Action || digest != i.Request.ParamsSHA256 || target != i.Request.Target.UUID || name != i.Request.Target.Name || session != i.Session || parent != i.ParentID {
		return false, errors.New("writer lease binding mismatch")
	}
	return stopped == 1, nil
}

// Child must return successfully before spawning the effect. Closure and parent
// completion serialize with this registration. Child ownership is independent.
func (j *Journal) Child(ctx context.Context, parent *Lease, id string, observer StopObserver) (*Lease, error) {
	if !textOK(id) || id == "root" || observer == nil {
		return nil, errors.New("child identity and observer required")
	}
	tx, err := j.write(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stopped, err := j.owner(ctx, tx, parent)
	if err != nil {
		return nil, err
	}
	if stopped {
		return nil, ErrBlocked
	}
	closed, err := targetRow(ctx, tx, parent.identity.Request.Target, false)
	if err != nil {
		return nil, err
	}
	if closed {
		return nil, ErrBlocked
	}
	if err = capacity(ctx, tx); err != nil {
		return nil, err
	}
	i := parent.identity
	i.WriterID = id
	i.ParentID = parent.identity.WriterID
	if _, err = tx.ExecContext(ctx, `INSERT INTO native_writers(request,writer,parent) VALUES(?,?,?)`, i.Request.ID, id, i.ParentID); err != nil {
		return nil, err
	}
	if err = revise(ctx, tx, i.Request.Target); err != nil {
		return nil, err
	}
	if err = j.commit(tx); err != nil {
		return nil, err
	}
	return &Lease{j, i, observer}, nil
}

// Complete invokes the observer bound at registration. Neither a caller-provided
// boolean nor an HTTP/operation status can complete a writer. A late completion
// updates only this exact writer, never another attempt or any descendant.
func (j *Journal) Complete(ctx context.Context, l *Lease) error {
	tx, err := j.write(ctx)
	if err != nil {
		return err
	}
	stopped, err := j.owner(ctx, tx, l)
	tx.Rollback()
	if err != nil {
		return err
	}
	if stopped {
		return nil
	}
	if err = l.observer.AwaitStopped(ctx, l.identity); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	tx, err = j.write(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stopped, err = j.owner(ctx, tx, l)
	if err != nil {
		return err
	}
	if stopped {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_writers SET stopped=1 WHERE request=? AND writer=?`, l.identity.Request.ID, l.identity.WriterID); err != nil {
		return err
	}
	if err = revise(ctx, tx, l.identity.Request.Target); err != nil {
		return err
	}
	return j.commit(tx)
}

// Cancel records intent only. It neither invokes native cancellation nor marks
// any writer stopped. The adapter still owes positive cessation evidence.
func (j *Journal) Cancel(ctx context.Context, l *Lease) error {
	tx, err := j.write(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = j.owner(ctx, tx, l); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_writers SET cancel_requested=1 WHERE request=? AND writer=?`, l.identity.Request.ID, l.identity.WriterID); err != nil {
		return err
	}
	if err = revise(ctx, tx, l.identity.Request.Target); err != nil {
		return err
	}
	return j.commit(tx)
}

// Seal is permanent and closes root AND child admission. It does not stop work.
func (j *Journal) Seal(ctx context.Context, t Target) error {
	tx, err := j.write(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	closed, err := targetRow(ctx, tx, t, true)
	if err != nil {
		return err
	}
	if closed {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_targets SET closed=1,revision=revision+1 WHERE uuid=?`, t.UUID); err != nil {
		return err
	}
	return j.commit(tx)
}
func (j *Journal) Evidence(ctx context.Context, t Target) (Evidence, error) {
	e := Evidence{Target: t, Scope: "registered writers only; production coverage and native cessation adapters unverified"}
	if err := j.usable(); err != nil {
		return e, err
	}
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return e, err
	}
	defer tx.Rollback()
	if err = validateJournal(ctx, tx); err != nil {
		return e, err
	}
	closed, err := targetRow(ctx, tx, t, false)
	if err != nil {
		return e, err
	}
	e.TargetAdmissionClosed = closed
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM native_targets WHERE uuid=?`, t.UUID).Scan(&e.Revision); err != nil {
		return e, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN w.stopped=0 THEN 1 ELSE 0 END),0) FROM native_writers w JOIN native_requests r ON r.id=w.request WHERE r.target=?`, t.UUID).Scan(&e.RegisteredWriters, &e.RegisteredWritersUnresolved); err != nil {
		return e, err
	}
	if err = tx.Commit(); err != nil {
		return e, err
	}
	if err = j.usable(); err != nil {
		return e, err
	}
	e.RegisteredWriterDrainVerified = closed && e.RegisteredWriters > 0 && e.RegisteredWritersUnresolved == 0
	// NativeQuiescenceVerified and WriterCoverageComplete deliberately remain false.
	return e, nil
}
