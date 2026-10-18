package nativevalidation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kvm-center/internal/nativework"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("/tmp", "kvm-power-tests-")
	if err != nil {
		panic(err)
	}
	powerScopeBase = root
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}
func (d *fakeDriver) PowerPersistent(context.Context, string) error { return nil }
func powerFixture(t *testing.T) (*PowerCoordinator, *fakeDriver) {
	t.Helper()
	v, j, _ := mutationFixture(t, "exit 0")
	p, err := NewPowerCoordinator(v, j, testID, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{}
	p.d = d
	p.qualified = true
	return p, d
}
func powerRequest(id, action string) PowerRequest {
	return PowerRequest{ID: id, Attempt: id + "-attempt", Action: action}
}
func executePower(t *testing.T, p *PowerCoordinator, id, action string) PowerResult {
	t.Helper()
	r, err := p.Execute(context.Background(), powerRequest(id, action))
	if err != nil || r.Status != "completed" {
		t.Fatalf("%s: %+v %v", action, r, err)
	}
	return r
}
func TestPowerEpochFiniteCyclesAndReplay(t *testing.T) {
	p, d := powerFixture(t)
	for cycle := 0; cycle < 3; cycle++ {
		for _, a := range []string{"define", "start", "destroy", "undefine"} {
			id := string(rune('a'+cycle)) + a
			r := executePower(t, p, id, a)
			if r.Replayed {
				t.Fatal(r)
			}
			before := len(d.calls)
			replay := executePower(t, p, id, a)
			if !replay.Replayed || replay.Epoch != r.Epoch || len(d.calls) != before {
				t.Fatal("replay redispatched")
			}
		}
	}
	root, _ := powerScope(p.v, p.id)
	h, err := p.read(root)
	if err != nil || len(h.Operations) != 12 {
		t.Fatal(h, err)
	}
	for _, o := range h.Operations {
		if len(o.Phases) != 5 {
			t.Fatal(o)
		}
	}
	e, err := p.previous.Evidence(context.Background(), nativework.Target{UUID: p.id.UUID, Name: p.id.Name})
	if err != nil || !e.TargetAdmissionClosed {
		t.Fatal(e, err)
	}
}
func TestPowerRecoveryAtEveryDurableBoundary(t *testing.T) {
	for _, phase := range []string{"adoption_intent", "adopted", "registered", "invocation_possible", "native_ack", "observed", "completed"} {
		t.Run(phase, func(t *testing.T) {
			p, d := powerFixture(t)
			fired := false
			p.afterSave = func(s string) error {
				if s == phase && !fired {
					fired = true
					return errors.New("lost persistence acknowledgement")
				}
				return nil
			}
			req := powerRequest("request", "define")
			_, err := p.Execute(context.Background(), req)
			if err == nil || !fired {
				t.Fatal("checkpoint not hit", err)
			}
			next := *p
			next.afterSave = nil // New coordinator, retained domain scope and data.
			before := len(d.calls)
			r, err := next.Execute(context.Background(), req)
			switch phase {
			case "invocation_possible":
				if !errors.Is(err, ErrPowerRecovery) || len(d.calls) != before {
					t.Fatal("unknown invocation retried", r, err)
				}
			case "registered":
				if err != nil || r.Status != "aborted" || len(d.calls) != before {
					t.Fatal("registered recovery not aborted", r, err)
				}
				executePower(t, &next, "new", "define")
			default:
				if err != nil || r.Status != "completed" {
					t.Fatal(r, err)
				}
				if (phase == "native_ack" || phase == "observed" || phase == "completed") && len(d.calls) != before {
					t.Fatal("recovery invoked mutation")
				}
			}
		})
	}
}
func TestPowerLostNativeAckIsTargetScoped(t *testing.T) {
	p, d := powerFixture(t)
	d.defineErr = true
	req := powerRequest("unknown", "define")
	if _, err := p.Execute(context.Background(), req); !errors.Is(err, ErrPowerRecovery) {
		t.Fatal(err)
	}
	d.defineErr = false
	before := len(d.calls)
	for _, r := range []PowerRequest{req, powerRequest("replacement", "start")} {
		if _, err := p.Execute(context.Background(), r); !errors.Is(err, ErrPowerRecovery) {
			t.Fatal(err)
		}
	}
	if len(d.calls) != before {
		t.Fatal("lost acknowledgement redispatched")
	}
	other := *p
	other.id = Identity{UUID: "22222222-2222-4222-8222-222222222222", Name: "kvmc-validation-other", Owner: strings.Repeat("d", 64)}
	other.d = &fakeDriver{}
	other.execution = hash("other-owned-domain")
	executePower(t, &other, "independent", "define")
}
func TestPowerCanonicalScopeBlocksLegacyAndAlternateHome(t *testing.T) {
	p, _ := powerFixture(t)
	executePower(t, p, "define", "define")
	otherJournal, err := nativework.Open(filepath.Join(p.v.Home, "different.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer otherJournal.Close()
	for _, changeHome := range []bool{false, true} {
		v := p.v
		if changeHome {
			v.Home = privateDir(t)
		}
		legacy, _, err := TrackVirshMutation(v, otherJournal, p.id, "legacy", "legacy-attempt", "start")
		if err != nil {
			t.Fatal(err)
		}
		if err = legacy.Start(context.Background(), p.id.UUID); err == nil {
			t.Fatal("legacy scope bypass")
		}
		alt, err := NewPowerCoordinator(v, otherJournal, p.id, p.profile)
		if err != nil {
			t.Fatal(err)
		}
		alt.qualified = true
		alt.d = p.d
		if _, err = alt.Execute(context.Background(), powerRequest("alternate", "start")); err == nil {
			t.Fatal("alternate scope adopted")
		}
	}
	noSpawn(t, p.v)
}
func TestPowerMissingOrMalformedHistoryNeverResets(t *testing.T) {
	for _, kind := range []string{"missing", "unknown", "duplicate", "truncated", "tmp"} {
		t.Run(kind, func(t *testing.T) {
			p, _ := powerFixture(t)
			executePower(t, p, "define", "define")
			root, _ := powerScope(p.v, p.id)
			path := filepath.Join(root, "epoch.json")
			switch kind {
			case "missing":
				os.Remove(path)
			case "unknown":
				os.WriteFile(path, []byte(`{"Version":99}`), 0600)
			case "duplicate":
				os.WriteFile(path, []byte(`{"Version":1,"version":1}`), 0600)
			case "truncated":
				os.WriteFile(path, []byte(`{`), 0600)
			case "tmp":
				os.WriteFile(filepath.Join(root, "epoch.tmp"), []byte(`{`), 0600)
			}
			if _, err := p.Execute(context.Background(), powerRequest("start", "start")); err == nil {
				t.Fatal("invalid history reset")
			}
		})
	}
}
func TestPowerRecoveryRequiresOwnershipAndPostcondition(t *testing.T) {
	p, d := powerFixture(t)
	p.afterSave = func(s string) error {
		if s == "native_ack" {
			return errors.New("crash")
		}
		return nil
	}
	req := powerRequest("define", "define")
	_, _ = p.Execute(context.Background(), req)
	p.afterSave = nil
	d.raw = strings.ReplaceAll(d.raw, p.id.Owner, strings.Repeat("f", 64))
	before := len(d.calls)
	if _, err := p.Execute(context.Background(), req); err == nil || len(d.calls) != before {
		t.Fatal("unowned recovery accepted")
	}
}
func TestPowerEpochLockExcludesLiveOwner(t *testing.T) {
	p, d := powerFixture(t)
	executePower(t, p, "define", "define")
	d.started = make(chan struct{})
	d.blockStart = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := p.Execute(context.Background(), powerRequest("start", "start")); done <- err }()
	<-d.started
	rival := *p
	if _, err := rival.Execute(context.Background(), powerRequest("rival", "destroy")); err == nil {
		t.Fatal("overlapping writer admitted")
	}
	close(d.blockStart)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	executePower(t, p, "destroy", "destroy")
}
func TestNativePowerProfileCannotBeCallerQualified(t *testing.T) {
	p, _ := powerFixture(t)
	p.qualified = false
	if _, err := p.Execute(context.Background(), powerRequest("define", "define")); !errors.Is(err, ErrPowerProfile) {
		t.Fatal(err)
	}
	noSpawn(t, p.v)
}

func TestPowerCancellationDoesNotManufactureAcknowledgement(t *testing.T) {
	p, d := powerFixture(t)
	executePower(t, p, "define", "define")
	d.started = make(chan struct{})
	d.blockStart = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := p.Execute(ctx, powerRequest("cancelled", "start")); done <- err }()
	<-d.started
	cancel()
	if err := <-done; !errors.Is(err, ErrPowerRecovery) {
		t.Fatal(err)
	}
	if _, err := p.Execute(context.Background(), powerRequest("next", "destroy")); !errors.Is(err, ErrPowerRecovery) {
		t.Fatal("cancel released domain", err)
	}
}
func TestPowerUnknownPredecessorCannotBeAdopted(t *testing.T) {
	p, _ := powerFixture(t)
	_, err := p.previous.Begin(context.Background(), nativework.Request{ID: "old", AttemptID: "old-attempt", Action: "VIRSH_START", ParamsSHA256: strings.Repeat("a", 64), Target: nativework.Target{UUID: p.id.UUID, Name: p.id.Name}}, unresolvedNativeObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Execute(context.Background(), powerRequest("new", "define")); !errors.Is(err, nativework.ErrBlocked) {
		t.Fatal("unknown predecessor adopted", err)
	}
	noSpawn(t, p.v)
}

func TestPowerEpochActualSubprocessPathAndDurableAckRecovery(t *testing.T) {
	body := `case "$3" in
 dumpxml) if [ -f "$HOME/domain.xml" ]; then /bin/cat "$HOME/domain.xml"; else exit 1; fi ;;
 list) if [ -f "$HOME/domain.xml" ]; then if [ "$5" = "--uuid" ]; then printf '%s\n' '11111111-1111-4111-8111-111111111111'; else printf '%s\n' 'kvmc-validation-test'; fi; fi ;;
 define) /bin/cat > "$HOME/domain.xml"; printf 'shut off\n' > "$HOME/state" ;;
 dominfo) printf 'Persistent: yes\nManaged save: no\nAutostart: disable\n' ;;
 domstate) /bin/cat "$HOME/state" ;;
 start) printf 'running\n' > "$HOME/state" ;;
 destroy) printf 'shut off\n' > "$HOME/state" ;;
 undefine) /bin/rm "$HOME/domain.xml" "$HOME/state" ;;
 *) exit 9 ;;
 esac`
	v, j, path := mutationFixture(t, body)
	p, err := NewPowerCoordinator(v, j, testID, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	p.qualified = true
	executePower(t, p, "define", "define")
	p.afterSave = func(phase string) error {
		if phase == "native_ack" {
			return errors.New("application died after durable native acknowledgement")
		}
		return nil
	}
	req := powerRequest("start", "start")
	if _, err = p.Execute(context.Background(), req); err == nil {
		t.Fatal("checkpoint not reached")
	}
	before, _ := os.ReadFile(filepath.Join(v.Home, "spawns"))
	if strings.Count(string(before), "\nstart\n") != 1 {
		t.Fatal("start count", string(before))
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativework.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	next, err := NewPowerCoordinator(v, reopened, testID, p.profile)
	if err != nil {
		t.Fatal(err)
	}
	next.qualified = true
	result, err := next.Execute(context.Background(), req)
	if err != nil || result.Status != "completed" || !result.Replayed {
		t.Fatal(result, err)
	}
	after, _ := os.ReadFile(filepath.Join(v.Home, "spawns"))
	if strings.Count(string(after), "\nstart\n") != 1 {
		t.Fatal("recovery redispatched start")
	}
	executePower(t, next, "destroy", "destroy")
	executePower(t, next, "undefine", "undefine")
	if _, err = os.Stat(filepath.Join(v.Home, "domain.xml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("synthetic fixture retained", err)
	}
}

func TestPowerLegacyPredecessorCannotBeReplaced(t *testing.T) {
	p, _ := powerFixture(t)
	v, _, err := TrackVirshMutation(p.v, p.previous, p.id, "legacy", "legacy-attempt", "define")
	if err != nil {
		t.Fatal(err)
	}
	if err = v.Define(context.Background(), XML(p.id)); !errors.Is(err, ErrNativeEffectsUnresolved) {
		t.Fatal(err)
	}
	other, err := nativework.Open(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	p.previous = other
	if _, err = p.Execute(context.Background(), powerRequest("fresh", "define")); err == nil {
		t.Fatal("alternate predecessor adopted unresolved legacy effects")
	}
}
func TestPowerHistoryRequiresCompleteCanonicalSchema(t *testing.T) {
	for _, mutation := range []string{"null", "omitted", "alias", "nested", "phases"} {
		t.Run(mutation, func(t *testing.T) {
			p, _ := powerFixture(t)
			p.afterSave = func(phase string) error {
				if phase == "invocation_possible" {
					return errors.New("crash")
				}
				return nil
			}
			_, _ = p.Execute(context.Background(), powerRequest("pending", "define"))
			root, _ := powerScope(p.v, p.id)
			path := filepath.Join(root, "epoch.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var obj map[string]json.RawMessage
			if err = json.Unmarshal(raw, &obj); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "null":
				obj["Operations"] = json.RawMessage("null")
			case "omitted":
				delete(obj, "Operations")
			case "alias":
				obj["operations"] = obj["Operations"]
				delete(obj, "Operations")
			case "nested", "phases":
				var ops []map[string]json.RawMessage
				_ = json.Unmarshal(obj["Operations"], &ops)
				if mutation == "nested" {
					ops[0]["Request"] = json.RawMessage("null")
				} else {
					ops[0]["Phases"] = json.RawMessage("null")
				}
				obj["Operations"], _ = json.Marshal(ops)
			}
			raw, _ = json.Marshal(obj)
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			p.afterSave = nil
			if _, err = p.Execute(context.Background(), powerRequest("new", "define")); err == nil {
				t.Fatal("malformed barrier accepted")
			}
		})
	}
	if validPowerRequest(PowerRequest{ID: string([]byte{255}), Attempt: "a", Action: "define"}) {
		t.Fatal("invalid UTF-8 accepted")
	}
}
