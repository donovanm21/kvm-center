package growguest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const vm = "11111111-1111-4111-8111-111111111111"

func request(s string) Request {
	return Request{Version: 1, ID: "synthetic-grow", Session: s, Target: Target{VMUUID: vm, DiskSerial: "synthetic-serial", FilesystemUUID: "22222222-2222-4222-8222-222222222222", FilesystemType: "ext4", Layout: "whole-disk"}, DesiredBytes: 8192}
}

type fakeAdapter struct {
	calls   int
	count   int
	alter   bool
	recheck error
	growErr error
	before  func()
}

func (a *fakeAdapter) Resolve(_ context.Context, t Target) ([]Binding, error) {
	if a.alter {
		t.DiskSerial = "other"
	}
	out := []Binding{}
	for i := 0; i < a.count; i++ {
		out = append(out, Binding{t, 4096, 16384})
	}
	return out, nil
}
func (a *fakeAdapter) Revalidate(context.Context, Binding) error {
	if a.before != nil {
		a.before()
	}
	return a.recheck
}
func (a *fakeAdapter) Grow(context.Context, Binding, uint64) (uint64, error) {
	a.calls++
	return 8192, a.growErr
}
func openTest(t *testing.T, path string) *Guest {
	t.Helper()
	g, e := OpenGuest(path, vm)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { g.Close() })
	return g
}
func TestDurableReplayRestartAndTargetTombstones(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.sqlite")
	g := openTest(t, path)
	r := request(g.Session())
	a := &fakeAdapter{count: 1}
	o, e := g.Run(context.Background(), r, a)
	if e != nil || o.Status != "adapter_reported" || o.NativeVerified {
		t.Fatalf("%+v %v", o, e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, e := g.Run(context.Background(), r, a)
			if e != nil || got != o {
				t.Error("lost-response replay changed")
			}
		}()
	}
	wg.Wait()
	g.Close()
	g = openTest(t, path)
	if g.Session() == r.Session {
		t.Fatal("session reused")
	}
	got, e := g.Run(context.Background(), r, a)
	if e != nil || got != o || a.calls != 1 {
		t.Fatal("restart executed again")
	}
	p, e := g.Report(context.Background())
	if e != nil || len(p.Outcomes) != 1 || p.Outcomes[0] != o || p.Session == o.Session {
		t.Fatal("restart lost retained outcome")
	}
	for _, kind := range []string{"id", "size", "serial", "fs"} {
		n := r
		n.Session = g.Session()
		n.ID = "new"
		switch kind {
		case "id":
			n.ID = r.ID
		case "size":
			n.DesiredBytes *= 2
		case "serial":
			n.Target.DiskSerial = "new"
		case "fs":
			n.Target.FilesystemUUID = vm
		}
		if _, e := g.Run(context.Background(), n, a); e == nil {
			t.Fatal("tombstone bypass", kind)
		}
	}
	if a.calls != 1 {
		t.Fatal("duplicate effect")
	}
}
func TestProductionRefusalAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    *fakeAdapter
		want string
	}{{"production", nil, "refused_native_unqualified"}, {"missing", &fakeAdapter{}, "refused_identity"}, {"ambiguous", &fakeAdapter{count: 2}, "refused_identity"}, {"changed", &fakeAdapter{count: 1, alter: true}, "refused_identity"}, {"revalidate", &fakeAdapter{count: 1, recheck: errors.New("changed")}, "refused_identity"}, {"uncertain", &fakeAdapter{count: 1, growErr: errors.New("uncertain")}, "recovery_required"}} {
		t.Run(tc.name, func(t *testing.T) {
			g := openTest(t, filepath.Join(t.TempDir(), "j"))
			r := request(g.Session())
			var a Adapter
			if tc.a != nil {
				a = tc.a
			}
			o, e := g.Run(context.Background(), r, a)
			if e != nil || o.Status != tc.want {
				t.Fatalf("%+v %v", o, e)
			}
			g.Run(context.Background(), r, a)
			if tc.a != nil && tc.a.calls > 1 {
				t.Fatal("duplicate")
			}
		})
	}
}
func TestJournalUncertaintyAndStaleRestore(t *testing.T) {
	for _, stage := range []string{"reserve", "outcome", "same-inode", "deleted", "replaced", "replace-before-grow", "rollback-before-grow", "terminal-before-grow"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "j")
			g := openTest(t, path)
			r := request(g.Session())
			a := &fakeAdapter{count: 1}
			commits := 0
			if stage == "reserve" || stage == "outcome" {
				g.afterCommit = func() error {
					commits++
					if stage == "reserve" || commits == 2 {
						return errors.New("lost ack")
					}
					return nil
				}
			}
			if stage == "terminal-before-grow" {
				a.before = func() {
					o := Result(r, "refused_identity", 0)
					state := State{1, []Record{{r, "reported", &o}}}
					raw, _ := json.Marshal(state)
					g.db.Exec(`UPDATE grow_ledger SET payload=?`, string(raw))
				}
			}
			if stage == "rollback-before-grow" {
				a.before = func() { g.db.Exec(`UPDATE grow_ledger SET payload='{"version":1,"records":[]}'`) }
			}
			if stage == "replace-before-grow" {
				a.before = func() { os.Rename(path, path+".old"); os.WriteFile(path, []byte{}, 0600) }
			}
			_, err := g.Run(context.Background(), r, a)
			switch stage {
			case "reserve":
				if err == nil || a.calls != 0 {
					t.Fatal("uncertain reservation executed")
				}
			case "outcome":
				if err == nil || a.calls != 1 {
					t.Fatal("bad outcome injection")
				}
			case "same-inode":
				if _, e := g.db.Exec(`UPDATE grow_ledger SET payload='{"version":1,"records":[]}'`); e != nil {
					t.Fatal(e)
				}
			case "deleted":
				os.Remove(path)
			case "replaced":
				os.Rename(path, path+".old")
				os.WriteFile(path, []byte{}, 0600)
			case "replace-before-grow", "rollback-before-grow", "terminal-before-grow":
				if a.calls != 0 || err == nil {
					t.Fatal("replacement allowed execution")
				}
			}
			before := a.calls
			if _, e := g.Run(context.Background(), r, a); e == nil {
				t.Fatal("not fenced")
			}
			if a.calls != before {
				t.Fatal("reexecuted")
			}
			if stage == "reserve" || stage == "outcome" {
				g.Close()
				g = openTest(t, path)
				o, e := g.Run(context.Background(), r, a)
				if e != nil || a.calls != before {
					t.Fatal("restart reexecuted", e)
				}
				if stage == "reserve" && o.Status != "recovery_required" {
					t.Fatal(o)
				}
			}
		})
	}
	g := openTest(t, filepath.Join(t.TempDir(), "fresh"))
	r := request(strings.Repeat("a", 64))
	a := &fakeAdapter{count: 1}
	if _, e := g.Run(context.Background(), r, a); e == nil || a.calls != 0 {
		t.Fatal("stale session accepted")
	}
	r = request(g.Session())
	r.Target.VMUUID = r.Target.FilesystemUUID
	if _, e := g.Run(context.Background(), r, a); e == nil {
		t.Fatal("wrong VM accepted")
	}
}
func TestStrictProtocolRefusals(t *testing.T) {
	raw, _ := json.Marshal(request(strings.Repeat("a", 64)))
	for _, s := range []string{strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1), strings.Replace(string(raw), `"version"`, `"Version"`, 1), strings.Replace(string(raw), `"desired_bytes":8192`, `"desired_bytes":null`, 1), strings.Replace(string(raw), `"whole-disk"`, `"partition"`, 1), strings.Replace(string(raw), `"desired_bytes":8192`, `"desired_bytes":"8192"`, 1), strings.Replace(string(raw), `"ext4"`, `"btrfs"`, 1), string(raw) + `{}`, strings.Replace(string(raw), `"target":{`, `"target":{"device":"/dev/fake",`, 1)} {
		var r Request
		if json.Unmarshal([]byte(s), &r) == nil {
			t.Fatal("accepted unsupported", s)
		}
	}
}

func TestJournalCorruptionAndHandleConcurrency(t *testing.T) {
	for _, payload := range []string{`{"version":99,"records":[]}`, `{"version":1,"records":null}`, `{"version":1,"records":[],"unknown":true}`} {
		t.Run(payload, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "j")
			g := openTest(t, path)
			if _, e := g.db.Exec(`UPDATE grow_ledger SET payload=?`, payload); e != nil {
				t.Fatal(e)
			}
			if _, e := g.Report(context.Background()); e == nil {
				t.Fatal("corruption accepted")
			}
			g.db.Exec(`UPDATE grow_ledger SET payload='{"version":1,"records":[]}'`)
			if _, e := g.Report(context.Background()); e == nil {
				t.Fatal("corruption fence cleared")
			}
			g.Close()
		})
	}
	path := filepath.Join(t.TempDir(), "j")
	one := openTest(t, path)
	two := openTest(t, path)
	r := request(one.Session())
	a := &fakeAdapter{count: 1}
	if _, e := one.Run(context.Background(), r, a); e != nil {
		t.Fatal(e)
	}
	if _, e := two.Run(context.Background(), r, a); e != nil || a.calls != 1 {
		t.Fatal("second handle reexecuted", e)
	}
}
