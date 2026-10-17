package nativevalidation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func uiFixture(t *testing.T) (UIWatchConfig, *fakeDriver) {
	t.Helper()
	p, err := filepath.EvalSymlinks(privateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return UIWatchConfig{testID, p}, &fakeDriver{}
}
func uiRead(t *testing.T, c UIWatchConfig) UIJournal {
	t.Helper()
	root, digest, e := uiConfig(c)
	if e != nil {
		t.Fatal(e)
	}
	j, e := uiJournal(root, digest, c.Identity)
	if e != nil {
		t.Fatal(e)
	}
	return j
}
func uiState(d *fakeDriver, state string) { d.mu.Lock(); defer d.mu.Unlock(); d.state = state }
func uiCalls(d *fakeDriver) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.calls, ",")
}

var shortUI = uiTiming{2 * time.Second, 300 * time.Millisecond, time.Millisecond}

func TestUIWatchdogExternalPowerSequence(t *testing.T) {
	c, d := uiFixture(t)
	phases := []string{}
	err := runUIWatchdog(context.Background(), c, d, false, func(j UIJournal) {
		phases = append(phases, j.Phase)
		disk := uiRead(t, c)
		if disk.Phase != j.Phase || disk.Domain.Fingerprint == "" {
			t.Fatal("ack before committed pin/phase")
		}
		switch j.Phase {
		case "PREPARED_STOPPED":
			if e := UISignal(c, "arm"); e != nil {
				t.Fatal(e)
			}
		case "ARMED":
			if j.ArmedDeadline.Sub(j.ArmedAt) != 45*time.Second {
				t.Fatal("deadline")
			}
			if e := UISignal(c, "arm"); e == nil {
				t.Fatal("rearm allowed")
			}
			uiState(d, "running")
		case "RUNNING_OBSERVED":
			uiState(d, "shut off")
		}
	}, shortUI)
	if err != nil {
		t.Fatal(err)
	}
	j := uiRead(t, c)
	if !j.Domain.Clean || j.Domain.Pending || j.Intervention || j.Result != "PASS_NATIVE_OBSERVATION" || j.RunningAt.IsZero() || j.StoppedAt.IsZero() {
		t.Fatalf("bad final journal %+v", j)
	}
	if uiCalls(d) != "define,undefine" {
		t.Fatal("helper powered guest", uiCalls(d))
	}
	if strings.Join(phases, ",") != "PREPARED_STOPPED,ARMED,RUNNING_OBSERVED,STOPPED_OBSERVED" {
		t.Fatal(phases)
	}
}
func TestUIWatchdogTimeoutAndCancellation(t *testing.T) {
	for _, mode := range []string{"never arm", "no start", "running timeout", "operator finish", "cancel", "early start"} {
		t.Run(mode, func(t *testing.T) {
			c, d := uiFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timing := uiTiming{250 * time.Millisecond, 200 * time.Millisecond, time.Millisecond}
			start := time.Now()
			e := runUIWatchdog(ctx, c, d, false, func(j UIJournal) {
				if j.Phase == "PREPARED_STOPPED" {
					switch mode {
					case "never arm":
					case "early start":
						uiState(d, "running")
					case "operator finish":
						if e := UISignal(c, "finish"); e != nil {
							t.Fatal(e)
						}
					case "cancel":
						cancel()
					default:
						if e := UISignal(c, "arm"); e != nil {
							t.Fatal(e)
						}
					}
				}
				if j.Phase == "ARMED" && mode == "running timeout" {
					uiState(d, "running")
				}
			}, timing)
			if e == nil {
				t.Fatal("failure reported success")
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("deadline extended")
			}
			j := uiRead(t, c)
			if !j.Domain.Clean {
				t.Fatal("cleanup not verified")
			}
			wantDestroy := mode == "running timeout" || mode == "early start"
			if j.Intervention != wantDestroy {
				t.Fatal("intervention mismatch")
			}
			if strings.Contains(uiCalls(d), "start") {
				t.Fatal("helper started")
			}
		})
	}
}
func TestUIWatchdogRefusesDriftCorruptJournalAndAmbiguousState(t *testing.T) {
	for _, mode := range []string{"XML", "journal", "unknown entry", "paused", "signal"} {
		t.Run(mode, func(t *testing.T) {
			c, d := uiFixture(t)
			e := runUIWatchdog(context.Background(), c, d, false, func(j UIJournal) {
				if j.Phase != "PREPARED_STOPPED" {
					return
				}
				switch mode {
				case "XML":
					d.mu.Lock()
					d.raw = strings.Replace(d.raw, "262144", "524288", 1)
					d.mu.Unlock()
				case "journal":
					if e := os.WriteFile(filepath.Join(c.StateDir, "journal.json"), []byte(`{"Version":1}`), 0600); e != nil {
						t.Fatal(e)
					}
				case "unknown entry":
					if e := os.WriteFile(filepath.Join(c.StateDir, "unexpected"), nil, 0600); e != nil {
						t.Fatal(e)
					}
				case "paused":
					uiState(d, "paused")
				case "signal":
					if e := os.WriteFile(filepath.Join(c.StateDir, "arm"), []byte(`{"ConfigSHA256":"wrong"}`), 0600); e != nil {
						t.Fatal(e)
					}
				}
			}, shortUI)
			if e == nil {
				t.Fatal("unsafe state accepted")
			}
			if mode != "signal" && uiCalls(d) != "define" {
				t.Fatal("destructive work despite refusal", uiCalls(d))
			}
			if mode == "signal" && uiCalls(d) != "define,undefine" {
				t.Fatal("signal refusal exact cleanup", uiCalls(d))
			}
		})
	}
}
func TestUIRecoveryNeverResumesAndRequiresReleasedLock(t *testing.T) {
	for _, state := range []string{"running", "shut off", "paused"} {
		t.Run(state, func(t *testing.T) {
			c, d := uiFixture(t)
			root, digest, e := uiConfig(c)
			if e != nil {
				t.Fatal(e)
			}
			lock, e := uiLock(root, false)
			if e != nil {
				t.Fatal(e)
			}
			j := UIJournal{Version: 1, ConfigSHA256: digest, Domain: Journal{Identity: testID, Pending: true}, Phase: "DEFINING", PreparedAt: time.Now(), Result: "NOT_COMPLETE"}
			if e = uiSave(root, j); e != nil {
				t.Fatal(e)
			}
			d.raw = normalizedXML(testID)
			d.state = state
			if e = RunUIWatchdog(context.Background(), c, d, true, nil); e == nil {
				t.Fatal("recovery competed with live lock")
			}
			if uiCalls(d) != "" {
				t.Fatal("mutated while locked")
			}
			lock.Close()
			e = RunUIWatchdog(context.Background(), c, d, true, nil)
			if state == "paused" {
				if e == nil || uiCalls(d) != "" {
					t.Fatal("paused recovery mutated")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			j = uiRead(t, c)
			if !j.Domain.Clean || j.Domain.Pending || j.Domain.Fingerprint != hash(normalizedXML(testID)) || strings.Contains(uiCalls(d), "start") {
				t.Fatal("recovery incorrect", j, uiCalls(d))
			}
		})
	}
}
func TestUIRecoveryCorruptionAndCleanReappearanceRefuse(t *testing.T) {
	for _, mode := range []string{"duplicate", "clean reappeared", "bad deadline", "missing journal", "wrong fingerprint"} {
		t.Run(mode, func(t *testing.T) {
			c, d := uiFixture(t)
			root, digest, configErr := uiConfig(c)
			if configErr != nil {
				t.Fatal(configErr)
			}
			lock, e := uiLock(root, false)
			if e != nil {
				t.Fatal(e)
			}
			lock.Close()
			d.raw = XML(testID)
			d.state = "shut off"
			j := UIJournal{Version: 1, ConfigSHA256: digest, Domain: Journal{Identity: testID, Fingerprint: hash(d.raw)}, Phase: "PREPARED_STOPPED", PreparedAt: time.Now()}
			switch mode {
			case "clean reappeared":
				j.Domain.Clean = true
				j.Phase = "CLEAN"
			case "bad deadline":
				j.ArmedAt = time.Now()
				j.ArmedDeadline = j.ArmedAt.Add(time.Hour)
			case "wrong fingerprint":
				j.Domain.Fingerprint = strings.Repeat("c", 64)
			}
			if mode != "missing journal" {
				if e = uiSave(root, j); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "duplicate" {
				b, _ := json.Marshal(j)
				b = append([]byte(`{"Version":2,`), b[1:]...)
				if e = os.WriteFile(filepath.Join(root, "journal.json"), b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if e = RunUIWatchdog(context.Background(), c, d, true, nil); e == nil {
				t.Fatal("unsafe recovery accepted")
			}
			if uiCalls(d) != "" {
				t.Fatal(uiCalls(d))
			}
		})
	}
}

type uiDeadlineDriver struct {
	*fakeDriver
	block bool
}

func (d *uiDeadlineDriver) State(ctx context.Context, id string) (string, error) {
	if d.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return d.fakeDriver.State(ctx, id)
}
func TestUIPollNativeCallsHonorPhaseDeadline(t *testing.T) {
	c, base := uiFixture(t)
	d := &uiDeadlineDriver{fakeDriver: base}
	start := time.Now()
	e := runUIWatchdog(context.Background(), c, d, false, func(j UIJournal) {
		if j.Phase == "PREPARED_STOPPED" {
			if e := UISignal(c, "arm"); e != nil {
				t.Fatal(e)
			}
		}
		if j.Phase == "ARMED" {
			d.block = true
		}
	}, uiTiming{time.Second, 50 * time.Millisecond, time.Millisecond})
	if e == nil {
		t.Fatal("deadline expected")
	}
	if time.Since(start) > CleanupBudget+2*time.Second {
		t.Fatal("unbounded call")
	}
}
func TestUIPrepareUncertainDefinitionAndAbsentErrors(t *testing.T) {
	c, d := uiFixture(t)
	d.defineErr = true
	if e := runUIWatchdog(context.Background(), c, d, false, nil, shortUI); e == nil {
		t.Fatal("uncertain define not reported")
	}
	if uiCalls(d) != "define,undefine" || !uiRead(t, c).Domain.Clean {
		t.Fatal("pending define cleanup")
	}
	c, d = uiFixture(t)
	d.collideName = true
	if e := runUIWatchdog(context.Background(), c, d, false, nil, shortUI); e == nil {
		t.Fatal("collision")
	}
	if uiCalls(d) != "" {
		t.Fatal("collision mutation")
	}
}
func TestUIPrivateSignalsAndManifest(t *testing.T) {
	c, _ := uiFixture(t)
	root, digest, configErr := uiConfig(c)
	if configErr != nil {
		t.Fatal(configErr)
	}
	j := UIJournal{Version: 1, ConfigSHA256: digest, Domain: Journal{Identity: testID, Fingerprint: strings.Repeat("a", 64)}, Phase: "PREPARED_STOPPED", PreparedAt: time.Now()}
	if e := uiSave(root, j); e != nil {
		t.Fatal(e)
	}
	if e := UISignal(c, "arm"); e != nil {
		t.Fatal(e)
	}
	if e := UISignal(c, "arm"); !errors.Is(e, os.ErrExist) {
		t.Fatal("duplicate signal allowed")
	}
	path := filepath.Join(root, "linked")
	if e := os.Link(filepath.Join(root, "arm"), path); e != nil {
		t.Fatal(e)
	}
	if _, e := privateUIFile(path); e == nil {
		t.Fatal("hardlink accepted")
	}
}

func TestUISignalPublicationIsAtomic(t *testing.T) {
	c, _ := uiFixture(t)
	root, digest, e := uiConfig(c)
	if e != nil {
		t.Fatal(e)
	}
	s := uiSignal{digest, c.Identity}
	if e = uiPublishSignal(root, "arm", s, func() error {
		if e := uiEntries(root, false); e != nil {
			t.Fatal(e)
		}
		present, e := uiReadSignal(root, "arm", digest, c.Identity)
		if e != nil || present {
			t.Fatal("partial marker visible", present, e)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	present, e := uiReadSignal(root, "arm", digest, c.Identity)
	if e != nil || !present {
		t.Fatal("committed marker missing", e)
	}
	if e = uiPublishSignal(root, "finish", s, func() error { return errors.New("interrupted before publish") }); e == nil {
		t.Fatal("injection missed")
	}
	if present, e = uiReadSignal(root, "finish", digest, c.Identity); e != nil || present {
		t.Fatal("interrupted marker exposed")
	}
}

type uiTransientGuardDriver struct {
	*fakeDriver
	badXML, badState bool
}

func (d *uiTransientGuardDriver) XML(ctx context.Context, id string) (string, error) {
	if d.badXML {
		d.badXML = false
		return strings.Replace(XML(testID), "262144", "524288", 1), nil
	}
	return d.fakeDriver.XML(ctx, id)
}
func (d *uiTransientGuardDriver) State(ctx context.Context, id string) (string, error) {
	if d.badState {
		d.badState = false
		return "paused", nil
	}
	return d.fakeDriver.State(ctx, id)
}
func TestUITransientGuardFailureNeverRetriesCleanup(t *testing.T) {
	for _, kind := range []string{"xml", "state"} {
		t.Run(kind, func(t *testing.T) {
			c, base := uiFixture(t)
			d := &uiTransientGuardDriver{fakeDriver: base}
			e := runUIWatchdog(context.Background(), c, d, false, func(j UIJournal) {
				if j.Phase == "PREPARED_STOPPED" {
					if kind == "xml" {
						d.badXML = true
					} else {
						d.badState = true
					}
				}
			}, shortUI)
			if e == nil || uiCalls(base) != "define" {
				t.Fatal("transient refusal retried destructively", e, uiCalls(base))
			}
		})
	}
}

func TestUISignalTemporaryDisappearanceDuringInventory(t *testing.T) {
	for _, name := range []string{"arm.tmp", "finish.tmp"} {
		t.Run(name, func(t *testing.T) {
			c, _ := uiFixture(t)
			root, digest, e := uiConfig(c)
			if e != nil {
				t.Fatal(e)
			}
			temp := filepath.Join(root, name)
			b, _ := json.Marshal(uiSignal{digest, c.Identity})
			if e = os.WriteFile(temp, b, 0600); e != nil {
				t.Fatal(e)
			}
			if e = uiEntriesObserved(root, false, func() {
				if err := os.Rename(temp, strings.TrimSuffix(temp, ".tmp")); err != nil {
					t.Fatal(err)
				}
			}); e != nil {
				t.Fatal("normal publication refused", e)
			}
			if e = os.Symlink(strings.TrimSuffix(temp, ".tmp"), temp); e != nil {
				t.Fatal(e)
			}
			if e = uiEntries(root, false); e == nil {
				t.Fatal("unsafe temp accepted")
			}
		})
	}
}
