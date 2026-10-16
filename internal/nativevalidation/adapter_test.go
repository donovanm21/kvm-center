package nativevalidation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"kvm-center/internal/agentauth"
	"kvm-center/internal/models"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testID = Identity{"11111111-1111-4111-8111-111111111111", "kvmc-validation-test", strings.Repeat("a", 64)}
var testSecret = strings.Repeat("b", 64)

type fakeDriver struct {
	mu                         sync.Mutex
	raw, state                 string
	calls                      []string
	collideName                bool
	defineErr, errorAfterStart bool
	failCleanup                bool
	blockStart                 chan struct{}
	started                    chan struct{}
}

func (d *fakeDriver) XML(_ context.Context, target string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.collideName && target == testID.Name {
		return XML(testID), nil
	}
	if d.raw == "" {
		return "", ErrAbsent
	}
	return d.raw, nil
}
func (d *fakeDriver) Define(_ context.Context, raw string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "define")
	d.raw = raw
	d.state = "shut off"
	if d.defineErr {
		return errors.New("uncertain definition")
	}
	return nil
}
func (d *fakeDriver) State(context.Context, string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state, nil
}
func (d *fakeDriver) Start(ctx context.Context, _ string) error {
	if d.started != nil {
		close(d.started)
	}
	if d.blockStart != nil {
		select {
		case <-d.blockStart:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "start")
	d.state = "running"
	if d.errorAfterStart {
		return errors.New("uncertain start")
	}
	return nil
}
func (d *fakeDriver) Destroy(context.Context, string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failCleanup {
		return errors.New("cleanup denied")
	}
	d.calls = append(d.calls, "destroy")
	d.state = "shut off"
	return nil
}
func (d *fakeDriver) Undefine(context.Context, string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failCleanup {
		return errors.New("cleanup denied")
	}
	d.calls = append(d.calls, "undefine")
	d.raw = ""
	return nil
}
func prepare(t *testing.T, d *fakeDriver) (*Adapter, *Journal) {
	t.Helper()
	j := new(Journal)
	a, e := Prepare(context.Background(), d, testID, testSecret, func(v Journal) error { *j = v; return nil })
	if e != nil {
		t.Fatal(e)
	}
	return a, j
}
func command(action, id string) models.CommandRequest {
	return models.CommandRequest{Action: action, RequestID: id, VMUUID: testID.UUID, VMName: testID.Name}
}
func signed(t *testing.T, a *Adapter, method, path string, body any, sign bool) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if sign {
		auth, exp, nonce, e := agentauth.Sign(testSecret, testID.Name, method, path, time.Now(), 0)
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set(agentauth.HeaderAuth, auth)
		r.Header.Set(agentauth.HeaderExp, exp)
		r.Header.Set(agentauth.HeaderNonce, nonce)
	}
	w := httptest.NewRecorder()
	a.Handler(func() {}).ServeHTTP(w, r)
	return w
}
func TestIdentityCollisionNeverMutates(t *testing.T) {
	for _, name := range []bool{false, true} {
		d := &fakeDriver{collideName: name}
		if !name {
			d.raw = XML(testID)
		}
		a, e := Prepare(context.Background(), d, testID, testSecret, func(Journal) error { t.Fatal("journal written on collision"); return nil })
		if e == nil || a != nil || len(d.calls) != 0 {
			t.Fatal("collision adopted")
		}
	}
}
func TestOwnedCommandsReportStateAndReplay(t *testing.T) {
	d := &fakeDriver{}
	a, j := prepare(t, d)
	if w := signed(t, a, "POST", "/command", command("START", "one"), false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, step := range []struct {
		action, id string
		state      models.VMState
	}{{"START", "one", models.VMRunning}, {"FORCESTOP", "two", models.VMStopped}} {
		w := signed(t, a, "POST", "/command", command(step.action, step.id), true)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		w = signed(t, a, "GET", "/validation/inventory", nil, true)
		var p models.TelemetryPayload
		if json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.VMs) != 1 || p.VMs[0].UUID != testID.UUID || p.VMs[0].State != step.state || len(p.Storage) != 0 {
			t.Fatal("incorrect owned inventory", w.Body.String())
		}
	}
	w := signed(t, a, "POST", "/command", command("START", "one"), true)
	var reply models.CommandResponse
	json.Unmarshal(w.Body.Bytes(), &reply)
	if w.Code != 200 || !reply.Replayed || len(d.calls) != 3 {
		t.Fatal("repeat executed")
	}
	if w = signed(t, a, "POST", "/command", command("FORCESTOP", "one"), true); w.Code != 409 {
		t.Fatal("conflicting request accepted")
	}
	if len(j.Requests) != 2 || j.Requests[0].Outcome != "success" {
		t.Fatal("requests not journaled")
	}
	if err := a.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !j.Clean || d.raw != "" {
		t.Fatal("cleanup incomplete")
	}
	if err := a.Cleanup(context.Background()); err != nil {
		t.Fatal("cleanup not idempotent", err)
	}
}
func TestNonOwnedAndUnsupportedOperationsRefused(t *testing.T) {
	d := &fakeDriver{}
	a, _ := prepare(t, d)
	bad := []models.CommandRequest{command("DELETE_VM", "x"), command("STOP", "x"), command("MIGRATE", "x"), command("START", "")}
	r := command("START", "x")
	r.VMUUID = "22222222-2222-4222-8222-222222222222"
	bad = append(bad, r)
	r = command("START", "x")
	r.VMName = "other"
	bad = append(bad, r)
	r = command("START", "x")
	r.Params = map[string]string{"disk": "/tmp/x"}
	bad = append(bad, r)
	for _, r := range bad {
		if w := signed(t, a, "POST", "/command", r, true); w.Code != 409 {
			t.Fatal(w.Code)
		}
	}
	for _, path := range []string{"/retire", "/self-update", "/storage", "/refresh-telemetry", "/command?x=1"} {
		if w := signed(t, a, "POST", path, nil, true); w.Code != 403 {
			t.Fatal(path, w.Code)
		}
	}
	if len(d.calls) != 1 {
		t.Fatal("forbidden operation mutated driver")
	}
}
func TestChangedOwnershipOrXMLStopsCleanup(t *testing.T) {
	for _, change := range []func(string) string{func(s string) string { return strings.Replace(s, testID.Owner, strings.Repeat("c", 64), 1) }, func(s string) string { return strings.Replace(s, "<vcpu>1</vcpu>", "<vcpu>2</vcpu>", 1) }, func(s string) string { return s + "\n" }, func(s string) string { return strings.Replace(s, "</devices>", "<disk/></devices>", 1) }} {
		d := &fakeDriver{}
		a, j := prepare(t, d)
		d.raw = change(d.raw)
		if _, err := a.command(context.Background(), command("START", "x")); err == nil {
			t.Fatal("changed config accepted")
		}
		if err := a.Cleanup(context.Background()); err == nil || j.Clean || len(d.calls) != 1 {
			t.Fatal("changed domain cleanup mutated")
		}
	}
}
func TestUncertainDefineAndStartRecovery(t *testing.T) {
	for _, define := range []bool{true, false} {
		d := &fakeDriver{defineErr: define, errorAfterStart: !define}
		var j Journal
		a, err := Prepare(context.Background(), d, testID, testSecret, func(v Journal) error { j = v; return nil })
		if define && err == nil {
			t.Fatal("expected define error")
		}
		if !define {
			_, err = a.command(context.Background(), command("START", "x"))
			if err == nil {
				t.Fatal("expected uncertain start")
			}
		}
		// Simulate process death: recovery loads only the pre-mutation journal and
		// never repeats START/define, including the pre-fingerprint crash window.
		recovered, err := Recover(d, j, func(v Journal) error { j = v; return nil })
		if err != nil {
			t.Fatal(err)
		}
		if err = recovered.Cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
		if d.raw != "" || !j.Clean {
			t.Fatal("residual resource")
		}
	}
}
func TestJournalFailurePreventsNativeMutation(t *testing.T) {
	d := &fakeDriver{}
	_, err := Prepare(context.Background(), d, testID, testSecret, func(Journal) error { return errors.New("disk failed") })
	if err == nil || len(d.calls) != 0 {
		t.Fatal("define preceded durable ownership")
	}
	a, _ := prepare(t, d)
	a.save = func(Journal) error { return errors.New("disk failed") }
	if _, err = a.command(context.Background(), command("START", "x")); err == nil || len(d.calls) != 1 {
		t.Fatal("start preceded journal")
	}
}
func TestCleanupFailureRetainsUnresolvedJournal(t *testing.T) {
	d := &fakeDriver{}
	a, j := prepare(t, d)
	d.failCleanup = true
	if err := a.Cleanup(context.Background()); err == nil || j.Clean {
		t.Fatal("cleanup falsely succeeded")
	}
}
func privateDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if e := os.Chmod(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}
func config(t *testing.T) Config {
	t.Helper()
	secret := filepath.Join(privateDir(t), "secret")
	if e := os.WriteFile(secret, []byte(testSecret), 0600); e != nil {
		t.Fatal(e)
	}
	return Config{OptIn: true, Identity: testID, StateDir: privateDir(t), SecretFile: secret, Listen: "127.0.0.1:0", Runtime: time.Second}
}
func TestRuntimeCancellationTimeoutAndRecovery(t *testing.T) {
	for _, reason := range []string{"cancel", "timeout"} {
		t.Run(reason, func(t *testing.T) {
			c := config(t)
			if reason == "timeout" {
				c.Runtime = 20 * time.Millisecond
			}
			d := &fakeDriver{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := Run(ctx, c, d, func(string) {
				if reason == "cancel" {
					cancel()
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if d.raw != "" {
				t.Fatal("domain remains")
			}
			raw, err := ReadPrivate(filepath.Join(c.StateDir, "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			var j Journal
			json.Unmarshal(raw, &j)
			if !j.Clean {
				t.Fatal("cleanup not persisted")
			}
			if err = Run(context.Background(), c, d, nil); err == nil {
				t.Fatal("normal restart reused state")
			}
			c.CleanupOnly = true
			if err = Run(context.Background(), c, d, nil); err != nil {
				t.Fatal("cleanup replay", err)
			}
		})
	}
}
func TestInterruptedInFlightActionCleansUp(t *testing.T) {
	c := config(t)
	d := &fakeDriver{blockStart: make(chan struct{}), started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan string, 1)
	go func() { done <- Run(ctx, c, d, func(addr string) { ready <- addr }) }()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("runtime readiness timeout")
	}
	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		raw, _ := json.Marshal(command("START", "x"))
		r, _ := http.NewRequest("POST", "http://"+addr+"/command", bytes.NewReader(raw))
		auth, exp, n, _ := agentauth.Sign(testSecret, testID.Name, "POST", "/command", time.Now(), 0)
		r.Header.Set(agentauth.HeaderAuth, auth)
		r.Header.Set(agentauth.HeaderExp, exp)
		r.Header.Set(agentauth.HeaderNonce, n)
		client := &http.Client{Timeout: time.Second}
		resp, _ := client.Do(r)
		if resp != nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-d.started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("command did not reach driver")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("interrupted in-flight action must report uncertainty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interruption did not drain")
	}
	<-reqDone
	if d.raw != "" {
		t.Fatal("interrupted domain remains")
	}
}
func TestRuntimeRejectsUnsafeConfigBeforeDriver(t *testing.T) {
	for _, change := range []func(*Config){func(c *Config) { c.OptIn = false }, func(c *Config) { c.Runtime = 46 * time.Second }, func(c *Config) { c.Listen = "0.0.0.0:9999" }, func(c *Config) { c.Listen = "127.0.0.1:8081" }, func(c *Config) { c.Identity.UUID = "other" }, func(c *Config) { os.WriteFile(filepath.Join(c.StateDir, "foreign"), nil, 0600) }, func(c *Config) { os.WriteFile(filepath.Join(c.StateDir, "owner.lock"), nil, 0600) }} {
		c := config(t)
		change(&c)
		d := &fakeDriver{}
		if err := Run(context.Background(), c, d, nil); err == nil || len(d.calls) != 0 {
			t.Fatal("unsafe configuration admitted")
		}
	}
}
func TestConcurrentCommandRetriesExecuteOnce(t *testing.T) {
	d := &fakeDriver{}
	a, _ := prepare(t, d)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := a.command(context.Background(), command("START", "same")); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if len(d.calls) != 2 {
		t.Fatal("multiple starts", d.calls)
	}
}

func TestCleanupReplayDiscardsOnlyPrivateUncommittedTemp(t *testing.T) {
	for _, kind := range []string{"partial", "symlink", "wrong-identity"} {
		t.Run(kind, func(t *testing.T) {
			c := config(t)
			d := &fakeDriver{}
			_, err := Prepare(context.Background(), d, testID, testSecret, func(j Journal) error { return saveJournal(c.StateDir, j) })
			if err != nil {
				t.Fatal(err)
			}
			temp := filepath.Join(c.StateDir, "journal.tmp")
			if kind == "symlink" {
				if err = os.Symlink(c.SecretFile, temp); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = os.WriteFile(temp, []byte(`{"Requests":`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			c.CleanupOnly = true
			if kind == "wrong-identity" {
				c.Identity.Owner = strings.Repeat("d", 64)
			}
			err = Run(context.Background(), c, d, nil)
			if kind == "partial" {
				if err != nil || d.raw != "" {
					t.Fatal("interrupted save blocked recovery", err)
				}
				if _, err = os.Lstat(temp); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("temp retained")
				}
			} else {
				if err == nil || len(d.calls) != 1 {
					t.Fatal("unsafe recovery admitted")
				}
				if _, err = os.Lstat(temp); err != nil {
					t.Fatal("unverified temp removed")
				}
			}
		})
	}
}
func TestFatalNativeCommandFailsRuntimeEvenAfterSuccessfulCleanup(t *testing.T) {
	c := config(t)
	d := &fakeDriver{errorAfterStart: true}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := Run(ctx, c, d, func(addr string) {
		raw, _ := json.Marshal(command("START", "uncertain"))
		r, _ := http.NewRequest("POST", "http://"+addr+"/command", bytes.NewReader(raw))
		auth, exp, n, _ := agentauth.Sign(testSecret, testID.Name, "POST", "/command", time.Now(), 0)
		r.Header.Set(agentauth.HeaderAuth, auth)
		r.Header.Set(agentauth.HeaderExp, exp)
		r.Header.Set(agentauth.HeaderNonce, n)
		client := &http.Client{Timeout: time.Second}
		resp, _ := client.Do(r)
		if resp != nil {
			resp.Body.Close()
		}
	})
	if err == nil {
		t.Fatal("fatal operation incorrectly returned success")
	}
	if d.raw != "" {
		t.Fatal("fatal operation skipped cleanup")
	}
	raw, e := ReadPrivate(filepath.Join(c.StateDir, "journal.json"))
	if e != nil {
		t.Fatal(e)
	}
	var j Journal
	json.Unmarshal(raw, &j)
	if !j.Clean || len(j.Requests) != 1 || j.Requests[0].Outcome != "pending" {
		t.Fatal("uncertainty erased")
	}
}
