package growguest

import (
	"context"
	"database/sql"
	"encoding/json"
	"kvm-center/internal/growprotocol"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// Wire aliases keep all validation in the dependency-light protocol package.
type Request = growprotocol.Request
type State = growprotocol.State
type Target = growprotocol.Target
type Outcome = growprotocol.Outcome
type Report = growprotocol.Report
type Record = growprotocol.Record

var ErrRefused = growprotocol.ErrRefused
var Session = growprotocol.Session
var Result = growprotocol.Result
var Transition = growprotocol.Transition
var Overlap = growprotocol.Overlap
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const journalDDL = `CREATE TABLE grow_ledger(id INTEGER PRIMARY KEY CHECK(id=1),payload TEXT NOT NULL)`

// Binding is an adapter-owned observation. No device path crosses the protocol.
// A qualified native implementation must hold exclusive device identity through
// revalidation and Grow; no such implementation is provided here.
type Binding struct {
	Target                       Target
	FilesystemBytes, DeviceBytes uint64
}
type Adapter interface {
	Resolve(context.Context, Target) ([]Binding, error)
	Revalidate(context.Context, Binding) error
	Grow(context.Context, Binding, uint64) (uint64, error)
}
type Guest struct {
	mu          sync.Mutex
	db          *sql.DB
	path        string
	identity    os.FileInfo
	session     string
	vmUUID      string
	floor       *State
	uncertain   bool
	afterCommit func() error // failure injection after actual durable commit
}

func OpenGuest(path, vmUUID string) (*Guest, error) {
	if !uuid.MatchString(vmUUID) {
		return nil, ErrRefused
	}
	if path == "" || strings.ContainsAny(path, "?\x00") || strings.HasPrefix(path, "file:") || path == ":memory:" {
		return nil, ErrRefused
	}
	p, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return nil, err
	}
	p = filepath.Join(parent, filepath.Base(p))
	st, statErr := os.Lstat(p)
	existed := statErr == nil
	if (statErr != nil && !os.IsNotExist(statErr)) || (existed && (!st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0)) {
		return nil, ErrRefused
	}
	if !existed {
		f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		if e = f.Close(); e != nil {
			return nil, e
		}
	}
	db, err := sql.Open("sqlite", p)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Guest, error) { _ = db.Close(); return nil, e }
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA synchronous=FULL"} {
		if _, err = db.Exec(q); err != nil {
			return fail(err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name NOT GLOB 'sqlite_*'`).Scan(&n); err != nil {
		return fail(err)
	}
	if n == 0 {
		if existed {
			return fail(ErrRefused)
		}
		if _, err = tx.Exec(journalDDL); err != nil {
			return fail(err)
		}
		if _, err = tx.Exec(`INSERT INTO grow_ledger VALUES(1,?)`, `{"version":1,"records":[]}`); err != nil {
			return fail(err)
		}
	}
	state, err := readGuest(tx)
	if err == nil {
		for _, r := range state.Records {
			if r.Request.Target.VMUUID != vmUUID {
				err = ErrRefused
			}
		}
	}
	if err != nil {
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	if err = os.Chmod(p, 0600); err != nil {
		return fail(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		return fail(err)
	}
	session, err := Session()
	if err != nil {
		return fail(err)
	}
	return &Guest{db: db, path: p, identity: info, session: session, vmUUID: vmUUID, floor: state.Clone()}, nil
}
func readGuest(tx *sql.Tx) (*State, error) {
	rows, err := tx.Query(`SELECT type,name,sql FROM sqlite_master WHERE name NOT GLOB 'sqlite_*'`)
	if err != nil {
		return nil, err
	}
	n := 0
	for rows.Next() {
		var kind, name, ddl string
		if rows.Scan(&kind, &name, &ddl) != nil || kind != "table" || name != "grow_ledger" || ddl != journalDDL {
			rows.Close()
			return nil, ErrRefused
		}
		n++
	}
	err = rows.Err()
	rows.Close()
	if err != nil || n != 1 {
		return nil, ErrRefused
	}
	var count int
	if tx.QueryRow(`SELECT COUNT(*) FROM grow_ledger`).Scan(&count) != nil || count != 1 {
		return nil, ErrRefused
	}
	var raw string
	if tx.QueryRow(`SELECT payload FROM grow_ledger WHERE id=1`).Scan(&raw) != nil {
		return nil, ErrRefused
	}
	var state State
	if json.Unmarshal([]byte(raw), &state) != nil {
		return nil, ErrRefused
	}
	for _, r := range state.Records {
		if r.Phase != "reserved" && r.Phase != "reported" {
			return nil, ErrRefused
		}
	}
	return &state, nil
}
func (g *Guest) continuity() error {
	st, err := os.Lstat(g.path)
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(g.identity, st) || g.uncertain {
		g.uncertain = true
		return ErrRefused
	}
	return nil
}
func (g *Guest) begin(ctx context.Context) (*sql.Tx, *State, error) {
	if g.continuity() != nil {
		return nil, nil, ErrRefused
	}
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE grow_ledger SET id=id`); err != nil {
		tx.Rollback()
		return nil, nil, err
	}
	s, err := readGuest(tx)
	if err == nil {
		for _, r := range s.Records {
			if r.Request.Target.VMUUID != g.vmUUID {
				err = ErrRefused
			}
		}
	}
	if err != nil {
		g.uncertain = true
		tx.Rollback()
		return nil, nil, err
	}
	if Transition(g.floor, s) != nil {
		tx.Rollback()
		g.uncertain = true
		return nil, nil, ErrRefused
	}
	g.floor = s.Clone()
	return tx, s, nil
}
func (g *Guest) commit(tx *sql.Tx, s *State) error {
	if s.Validate() != nil {
		return ErrRefused
	}
	raw, err := json.Marshal(s)
	if err == nil {
		_, err = tx.Exec(`UPDATE grow_ledger SET payload=? WHERE id=1`, string(raw))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err == nil && g.afterCommit != nil {
		err = g.afterCommit()
	}
	if err != nil {
		g.uncertain = true
		return ErrRefused
	}
	g.floor = s.Clone()
	return g.continuity()
}
func (g *Guest) Session() string { return g.session }
func (g *Guest) VMUUID() string  { return g.vmUUID }
func (g *Guest) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.uncertain = true
	return g.db.Close()
}
func (g *Guest) Report(ctx context.Context) (Report, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	tx, s, err := g.begin(ctx)
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()
	p := Report{Version: 1, Session: g.session, Outcomes: []Outcome{}}
	for _, r := range s.Records {
		if r.Outcome != nil {
			p.Outcomes = append(p.Outcomes, *r.Outcome)
		} else {
			p.Outcomes = append(p.Outcomes, Result(r.Request, "recovery_required", 0))
		}
	}
	return p, nil
}
func (g *Guest) Run(ctx context.Context, r Request, a Adapter) (Outcome, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Validate() != nil || r.Target.VMUUID != g.vmUUID {
		return Outcome{}, ErrRefused
	}
	tx, s, err := g.begin(ctx)
	if err != nil {
		return Outcome{}, err
	}
	defer tx.Rollback()
	for _, old := range s.Records {
		if old.Request.ID == r.ID {
			if old.Request != r {
				return Outcome{}, ErrRefused
			}
			if old.Outcome != nil {
				return *old.Outcome, nil
			}
			return Result(r, "recovery_required", 0), nil
		}
		if Overlap(old.Request.Target, r.Target) {
			return Outcome{}, ErrRefused
		}
	}
	if r.Session != g.session {
		return Outcome{}, ErrRefused
	}
	s.Records = append(s.Records, Record{Request: r, Phase: "reserved"})
	if err = g.commit(tx, s); err != nil {
		return Outcome{}, err
	}
	// The production caller supplies nil: durable explicit refusal, no discovery
	// commands or filesystem tools. Only synthetic tests supply an adapter.
	result := Result(r, "refused_native_unqualified", 0)
	if a != nil {
		matches, e := a.Resolve(ctx, r.Target)
		if e != nil || len(matches) != 1 || matches[0].Target != r.Target || matches[0].FilesystemBytes >= r.DesiredBytes || matches[0].DeviceBytes < r.DesiredBytes || a.Revalidate(ctx, matches[0]) != nil {
			result = Result(r, "refused_identity", 0)
		} else {
			check, current, err := g.begin(ctx)
			if err != nil {
				return Outcome{}, ErrRefused
			}
			found := false
			for _, entry := range current.Records {
				if entry.Request == r && entry.Phase == "reserved" && entry.Outcome == nil {
					found = true
				}
			}
			if !found {
				check.Rollback()
				g.uncertain = true
				return Outcome{}, ErrRefused
			}
			// Keep the writer reservation through the adapter call. Cooperating journal
			// handles cannot mutate its reservation between validation and the effect.
			size, e := a.Grow(ctx, matches[0], r.DesiredBytes)
			check.Rollback()
			result = Result(r, "recovery_required", 0)
			if e == nil && size >= r.DesiredBytes && size <= matches[0].DeviceBytes {
				result = Result(r, "adapter_reported", size)
			}
		}
	}
	tx, s, err = g.begin(ctx)
	if err != nil {
		return Outcome{}, err
	}
	defer tx.Rollback()
	i := -1
	// Another handle cannot replace or rewrite this reserved request.
	for n, old := range s.Records {
		if old.Request.ID == r.ID {
			i = n
		}
	}
	if i < 0 || s.Records[i].Request != r || s.Records[i].Phase != "reserved" {
		return Outcome{}, ErrRefused
	}
	s.Records[i].Phase = "reported"
	s.Records[i].Outcome = &result
	if err = g.commit(tx, s); err != nil {
		return Outcome{}, err
	}
	return result, nil
}
