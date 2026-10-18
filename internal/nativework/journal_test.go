package nativework

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var ctx = context.Background()
var target = Target{"11111111-1111-4111-8111-111111111111", "synthetic-one"}

func request(id string) Request {
	return Request{ID: id, AttemptID: "attempt-" + id, Action: "MIGRATE", ParamsSHA256: strings.Repeat("a", 64), Target: target}
}

type syntheticObserver struct {
	stopped atomic.Bool
	calls   atomic.Int32
	expect  Identity
}

func (o *syntheticObserver) AwaitStopped(_ context.Context, i Identity) error {
	o.calls.Add(1)
	if i != o.expect {
		return errors.New("foreign identity")
	}
	if !o.stopped.Load() {
		return errors.New("synthetic native writer still active")
	}
	return nil
}
func openTest(t *testing.T) (*Journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "writers.db")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j, path
}
func begin(t *testing.T, j *Journal, id string) (*Lease, *syntheticObserver) {
	t.Helper()
	o := &syntheticObserver{}
	l, err := j.Begin(ctx, request(id), o)
	if err != nil {
		t.Fatal(err)
	}
	o.expect = l.Identity()
	return l, o
}
func evidence(t *testing.T, j *Journal) Evidence {
	t.Helper()
	e, err := j.Evidence(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if e.NativeQuiescenceVerified || e.WriterCoverageComplete {
		t.Fatal("invented complete native coverage")
	}
	return e
}
func stop(t *testing.T, j *Journal, l *Lease, o *syntheticObserver) {
	t.Helper()
	o.stopped.Store(true)
	if err := j.Complete(ctx, l); err != nil {
		t.Fatal(err)
	}
}

func TestAttemptBindingOverlapAndLateCompletion(t *testing.T) {
	j, _ := openTest(t)
	l, o := begin(t, j, "first")
	if _, err := j.Begin(ctx, request("first"), o); !errors.Is(err, ErrReplay) {
		t.Fatal("same request redispatched", err)
	}
	for _, edit := range []func(*Request){func(r *Request) { r.AttemptID = "new-attempt" }, func(r *Request) { r.Action = "START" }, func(r *Request) { r.ParamsSHA256 = strings.Repeat("b", 64) }, func(r *Request) { r.Target.Name = "other" }} {
		r := request("first")
		edit(&r)
		if _, err := j.Begin(ctx, r, o); err == nil {
			t.Fatal("changed binding accepted")
		}
	}
	if _, err := j.Begin(ctx, request("second"), o); !errors.Is(err, ErrBlocked) {
		t.Fatal("overlapping new attempt admitted", err)
	}
	if err := j.Complete(ctx, l); err == nil {
		t.Fatal("command return without native stop accepted")
	}
	stop(t, j, l, o)
	next, nextObserver := begin(t, j, "second")
	// A late duplicate completion for the old attempt cannot clear the new writer.
	if err := j.Complete(ctx, l); err != nil {
		t.Fatal(err)
	}
	if e := evidence(t, j); e.RegisteredWritersUnresolved != 1 || e.RegisteredWriterDrainVerified {
		t.Fatal(e)
	}
	stop(t, j, next, nextObserver)
	if e := evidence(t, j); e.RegisteredWriterDrainVerified {
		t.Fatal("open admission called drained")
	}
	if err := j.Seal(ctx, target); err != nil {
		t.Fatal(err)
	}
	if e := evidence(t, j); !e.RegisteredWriterDrainVerified || e.RegisteredWriters != 2 {
		t.Fatal(e)
	}
	if _, err := j.Begin(ctx, request("third"), o); !errors.Is(err, ErrBlocked) {
		t.Fatal("late request escaped closure", err)
	}
}
func TestDescendantCancellationAndPositiveObservation(t *testing.T) {
	j, _ := openTest(t)
	root, ro := begin(t, j, "root-request")
	co := &syntheticObserver{}
	child, err := j.Child(ctx, root, "transfer", co)
	if err != nil {
		t.Fatal(err)
	}
	co.expect = child.Identity()
	if err = j.Cancel(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err = j.Cancel(ctx, child); err != nil {
		t.Fatal(err)
	}
	if err = j.Seal(ctx, target); err != nil {
		t.Fatal(err)
	}
	stop(t, j, root, ro)
	if e := evidence(t, j); e.RegisteredWritersUnresolved != 1 || e.RegisteredWriterDrainVerified {
		t.Fatal("root/cancel released child", e)
	}
	if _, err = j.Child(ctx, root, "late", co); err == nil {
		t.Fatal("stopped root created child")
	}
	if _, err = j.Child(ctx, child, "late", co); err == nil {
		t.Fatal("closed target created descendant")
	}
	if err = j.Complete(ctx, child); err == nil {
		t.Fatal("cancel implied stopped")
	}
	stop(t, j, child, co)
	before := evidence(t, j)
	if !before.RegisteredWriterDrainVerified {
		t.Fatal(before)
	}
	if err = j.Complete(ctx, child); err != nil {
		t.Fatal(err)
	}
	after := evidence(t, j)
	if before != after {
		t.Fatal("duplicate receipt changed revision")
	}
}
func TestRestartAndForeignOwnerCannotResolve(t *testing.T) {
	j, path := openTest(t)
	l, o := begin(t, j, "uncertain")
	if err := j.Seal(ctx, target); err != nil {
		t.Fatal(err)
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	o.stopped.Store(true)
	if err = other.Complete(ctx, l); err == nil {
		t.Fatal("foreign handle borrowed owner")
	}
	if o.calls.Load() != 0 {
		t.Fatal("foreign owner invoked adapter")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	if err = j.Complete(ctx, l); err == nil {
		t.Fatal("closed owner completed")
	}
	if e := evidence(t, other); e.RegisteredWritersUnresolved != 1 || e.RegisteredWriterDrainVerified {
		t.Fatal("restart inferred stop", e)
	}
	if _, err = other.Begin(ctx, request("retry"), o); err == nil {
		t.Fatal("restart inferred replacement ownership")
	}
}
func TestConcurrentClosureAndRegistration(t *testing.T) {
	for n := 0; n < 12; n++ {
		t.Run(string(rune('a'+n)), func(t *testing.T) {
			j, path := openTest(t)
			other, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			root, ro := begin(t, j, "owner")
			co := &syntheticObserver{}
			start := make(chan struct{})
			var child *Lease
			var childErr, sealErr error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); <-start; child, childErr = j.Child(ctx, root, "child", co) }()
			go func() { defer wg.Done(); <-start; sealErr = other.Seal(ctx, target) }()
			close(start)
			wg.Wait()
			if sealErr != nil {
				t.Fatal(sealErr)
			}
			stop(t, j, root, ro)
			if childErr == nil {
				co.expect = child.Identity()
				if e := evidence(t, other); e.RegisteredWritersUnresolved != 1 {
					t.Fatal(e)
				}
				stop(t, j, child, co)
			}
			if e := evidence(t, other); !e.RegisteredWriterDrainVerified {
				t.Fatal(e)
			}
			if _, err = j.Child(ctx, root, "late", co); err == nil {
				t.Fatal("late child")
			}
		})
	}
}
func TestConcurrentParentStopAndChildRegistration(t *testing.T) {
	j, _ := openTest(t)
	root, ro := begin(t, j, "owner")
	ro.stopped.Store(true)
	co := &syntheticObserver{}
	var child *Lease
	var childErr, stopErr error
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	go func() { defer wg.Done(); <-start; child, childErr = j.Child(ctx, root, "child", co) }()
	go func() { defer wg.Done(); <-start; stopErr = j.Complete(ctx, root) }()
	close(start)
	wg.Wait()
	if stopErr != nil {
		t.Fatal(stopErr)
	}
	if err := j.Seal(ctx, target); err != nil {
		t.Fatal(err)
	}
	if childErr == nil {
		co.expect = child.Identity()
		if e := evidence(t, j); e.RegisteredWritersUnresolved != 1 {
			t.Fatal(e)
		}
		stop(t, j, child, co)
	}
	if e := evidence(t, j); !e.RegisteredWriterDrainVerified {
		t.Fatal(e)
	}
}
func TestCommitAcknowledgementLossFencesHandle(t *testing.T) {
	for _, stage := range []string{"begin", "complete", "seal"} {
		t.Run(stage, func(t *testing.T) {
			j, path := openTest(t)
			var l *Lease
			var o *syntheticObserver
			if stage != "begin" {
				l, o = begin(t, j, "owner")
			}
			if stage == "complete" {
				if err := j.Seal(ctx, target); err != nil {
					t.Fatal(err)
				}
				o.stopped.Store(true)
			}
			j.afterCommit = func() error { return errors.New("synthetic acknowledgement loss") }
			var err error
			switch stage {
			case "begin":
				l, err = j.Begin(ctx, request("owner"), &syntheticObserver{})
				if l != nil {
					t.Fatal("uncertain begin returned permission")
				}
			case "complete":
				err = j.Complete(ctx, l)
			case "seal":
				err = j.Seal(ctx, target)
			}
			if !errors.Is(err, ErrUncertain) {
				t.Fatal(err)
			}
			if _, err = j.Evidence(ctx, target); !errors.Is(err, ErrUncertain) {
				t.Fatal("uncertain handle gave evidence", err)
			}
			if err = j.Close(); err != nil {
				t.Fatal(err)
			}
			recovered, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			e := evidence(t, recovered)
			if stage == "complete" {
				if !e.RegisteredWriterDrainVerified {
					t.Fatal("committed positive observation lost")
				}
			} else if e.RegisteredWriterDrainVerified || e.RegisteredWritersUnresolved != 1 {
				t.Fatal("recovery invented cessation", e)
			}
		})
	}
}
func TestUnknownVersionIdentityAndEmptyCoverage(t *testing.T) {
	j, path := openTest(t)
	if err := j.Seal(ctx, target); err != nil {
		t.Fatal(err)
	}
	if e := evidence(t, j); e.RegisteredWriterDrainVerified || e.RegisteredWriters != 0 {
		t.Fatal("empty registered set claimed complete drain")
	}
	for _, bad := range []Target{{target.UUID, "renamed"}, {"22222222-2222-4222-8222-222222222222", target.Name}, {"22222222-2222-4222-8222-222222222222", target.UUID}, {target.UUID, "unsupported name"}} {
		if err := j.Seal(ctx, bad); err == nil {
			t.Fatal("conflicting/unsupported identity accepted", bad)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE native_meta SET version=99`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if x, err := Open(path); err == nil {
		x.Close()
		t.Fatal("unknown version loaded")
	}
}

func TestMalformedJournalNeverProducesDrain(t *testing.T) {
	for _, mutation := range []string{
		`ALTER TABLE native_writers ADD COLUMN invented TEXT`,
		`DROP TABLE native_writers; CREATE TABLE native_writers(request TEXT,writer TEXT,parent TEXT,stopped INTEGER,cancel_requested INTEGER)`,
		`CREATE TRIGGER invented AFTER INSERT ON native_writers BEGIN UPDATE native_writers SET stopped=1; END`,
		`CREATE TRIGGER sqliteXstop AFTER INSERT ON native_writers BEGIN UPDATE native_writers SET stopped=1; END`,
		`INSERT INTO native_writers(request,writer,parent) VALUES('orphan','root','')`,
		`DELETE FROM native_writers`,
		`UPDATE native_writers SET stopped=2`,
		`UPDATE native_writers SET cancel_requested=2`,
		`UPDATE native_writers SET parent='root',writer='cycle'`,
		`UPDATE native_requests SET target='22222222-2222-4222-8222-222222222222'`,
		`UPDATE native_requests SET session='unknown'`,
		`UPDATE native_targets SET closed=2`,
	} {
		t.Run(mutation, func(t *testing.T) {
			j, path := openTest(t)
			l, o := begin(t, j, "request")
			stop(t, j, l, o)
			if err := j.Seal(ctx, target); err != nil {
				t.Fatal(err)
			}
			if _, err := j.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := j.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if e, err := j.Evidence(ctx, target); err == nil || e.RegisteredWriterDrainVerified {
				t.Fatal("malformed journal certified drain", e, err)
			}
			if _, err := j.Begin(ctx, request("next"), o); err == nil {
				t.Fatal("malformed journal admitted writer")
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(path); err == nil {
				reopened.Close()
				t.Fatal("malformed journal reopened")
			}
		})
	}
}

type delayedObserver struct {
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	identity Identity
}

func (o *delayedObserver) AwaitStopped(_ context.Context, i Identity) error {
	if i != o.identity {
		return errors.New("wrong identity")
	}
	o.once.Do(func() { close(o.entered) })
	<-o.release
	return nil
}
func TestLateObservationAfterCancellationCannotRelease(t *testing.T) {
	j, _ := openTest(t)
	o := &delayedObserver{entered: make(chan struct{}), release: make(chan struct{})}
	l, err := j.Begin(ctx, request("late"), o)
	if err != nil {
		t.Fatal(err)
	}
	o.identity = l.Identity()
	pending, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { result <- j.Complete(pending, l) }()
	<-o.entered
	if err = j.Cancel(ctx, l); err != nil {
		t.Fatal(err)
	}
	if err = j.Seal(ctx, target); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err = j.Begin(ctx, request("overlap"), o); err == nil {
		t.Fatal("overlap admitted during pending native cessation")
	}
	close(o.release)
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("expired observation accepted", err)
	}
	if e := evidence(t, j); e.RegisteredWritersUnresolved != 1 || e.RegisteredWriterDrainVerified {
		t.Fatal(e)
	}
	if err = j.Complete(ctx, l); err != nil {
		t.Fatal(err)
	}
	if e := evidence(t, j); !e.RegisteredWriterDrainVerified {
		t.Fatal(e)
	}
}

func TestClosedJournalCopyPreservesScopedEvidenceAndReplay(t *testing.T) {
	j, path := openTest(t)
	l, o := begin(t, j, "completed")
	stop(t, j, l, o)
	if err := j.Seal(ctx, target); err != nil {
		t.Fatal(err)
	}
	want := evidence(t, j)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "retained-copy.db")
	if err = os.WriteFile(copyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := evidence(t, restored); got != want {
		t.Fatal("copy changed closure evidence", got, want)
	}
	if _, err = restored.Begin(ctx, request("completed"), o); !errors.Is(err, ErrReplay) {
		t.Fatal("copy replayed", err)
	}
	if _, err = restored.Begin(ctx, request("late"), o); err == nil {
		t.Fatal("copy reopened sealed target")
	}
}
