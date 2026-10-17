package nativevalidation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// UIWatchConfig is deliberately credential-free. The production agent owns only
// the externally requested START/FORCESTOP; this helper never starts a domain.
type UIWatchConfig struct {
	Identity Identity
	StateDir string
}
type UIJournal struct {
	Version       int
	ConfigSHA256  string
	Domain        Journal
	Phase         string
	PreparedAt    time.Time
	ArmedAt       time.Time
	ArmedDeadline time.Time
	RunningAt     time.Time
	StoppedAt     time.Time
	Intervention  bool
	Result        string
}
type uiSignal struct {
	ConfigSHA256 string
	Identity     Identity
}
type uiTiming struct{ prepare, armed, poll time.Duration }

var uiBudgets = uiTiming{120 * time.Second, 45 * time.Second, 250 * time.Millisecond}

func privateUIFile(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 1<<20 {
		return nil, errors.New("private regular0600 file required")
	}
	v := reflect.ValueOf(st.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return nil, errors.New("link count unavailable")
	}
	n := v.FieldByName("Nlink")
	if !n.IsValid() || !n.CanUint() || n.Uint() != 1 {
		return nil, errors.New("hardlinked file refused")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return nil, errors.New("file changed during open")
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if len(b) > 1<<20 {
		return nil, errors.New("file too large")
	}
	return b, err
}
func strictUIJSON(raw []byte, v any) error {
	// Reject duplicate keys recursively before ordinary struct decoding.
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 16 {
			return errors.New("JSON nesting refused")
		}
		t, e := d.Token()
		if e != nil {
			return e
		}
		x, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch x {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				t, e := d.Token()
				if e != nil {
					return e
				}
				k, ok := t.(string)
				if !ok {
					return errors.New("invalid key")
				}
				fold := strings.ToLower(k)
				if seen[fold] {
					return errors.New("duplicate/ambiguous key")
				}
				seen[fold] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		case '[':
			for d.More() {
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
			_, e = d.Token()
			return e
		}
		return errors.New("unexpected delimiter")
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func uiConfig(c UIWatchConfig) (string, string, error) {
	if err := c.Identity.Validate(); err != nil {
		return "", "", err
	}
	root, err := privateRoot(c.StateDir)
	if err != nil {
		return "", "", err
	}
	if root != filepath.Clean(c.StateDir) {
		return "", "", errors.New("symlink state path refused")
	}
	st, err := os.Stat(root)
	if err != nil || st.Mode().Perm() != 0700 {
		return "", "", errors.New("state mode must be0700")
	}
	b, _ := json.Marshal(c)
	return root, hash(string(b)), nil
}
func uiSave(root string, j UIJournal) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "journal.tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err = os.Rename(f.Name(), filepath.Join(root, "journal.json")); err != nil {
		return err
	}
	return uiSync(root)
}
func uiSync(root string) error {
	f, e := os.Open(root)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func uiJournal(root, digest string, id Identity) (UIJournal, error) {
	var j UIJournal
	b, err := privateUIFile(filepath.Join(root, "journal.json"))
	if err != nil {
		return j, err
	}
	if err = strictUIJSON(b, &j); err != nil {
		return j, err
	}
	if j.Version != 1 || j.ConfigSHA256 != digest || j.Domain.Identity != id || len(j.Domain.Requests) != 0 || j.PreparedAt.IsZero() {
		return j, errors.New("journal identity/schema mismatch")
	}
	switch j.Phase {
	case "DEFINING", "PREPARED_STOPPED", "ARMED", "RUNNING_OBSERVED", "STOPPED_OBSERVED", "CLEAN":
	default:
		return j, errors.New("unknown journal phase")
	}
	if j.Domain.Fingerprint != "" && !ownerPattern.MatchString(j.Domain.Fingerprint) {
		return j, errors.New("invalid fingerprint")
	}
	if j.Domain.Pending {
		if j.Phase != "DEFINING" || j.Domain.Fingerprint != "" || j.Domain.Clean {
			return j, errors.New("invalid pending journal")
		}
	} else if j.Domain.Fingerprint == "" && !j.Domain.Clean {
		return j, errors.New("missing committed fingerprint")
	}
	if j.Domain.Clean != (j.Phase == "CLEAN") {
		return j, errors.New("inconsistent clean phase")
	}
	if !j.ArmedAt.IsZero() {
		if j.ArmedDeadline.Sub(j.ArmedAt) != 45*time.Second {
			return j, errors.New("invalid fixed armed deadline")
		}
	} else if !j.ArmedDeadline.IsZero() || !j.RunningAt.IsZero() || !j.StoppedAt.IsZero() {
		return j, errors.New("unarmed observation")
	}
	if (j.Phase == "ARMED" || j.Phase == "RUNNING_OBSERVED" || j.Phase == "STOPPED_OBSERVED") && j.ArmedAt.IsZero() {
		return j, errors.New("missing armed state")
	}
	if (j.Phase == "RUNNING_OBSERVED" || j.Phase == "STOPPED_OBSERVED") && j.RunningAt.IsZero() {
		return j, errors.New("running observation absent")
	}
	if j.Phase == "STOPPED_OBSERVED" && j.StoppedAt.IsZero() {
		return j, errors.New("stopped observation absent")
	}
	if !j.RunningAt.IsZero() && j.RunningAt.Before(j.ArmedAt) {
		return j, errors.New("invalid running observation time")
	}
	if !j.StoppedAt.IsZero() && (j.RunningAt.IsZero() || j.StoppedAt.Before(j.RunningAt)) {
		return j, errors.New("invalid stopped observation time")
	}
	if (j.Phase == "DEFINING" || j.Phase == "PREPARED_STOPPED") && !j.ArmedAt.IsZero() {
		return j, errors.New("premature armed fields")
	}
	if j.Phase == "ARMED" && (!j.RunningAt.IsZero() || !j.StoppedAt.IsZero()) {
		return j, errors.New("premature observations")
	}
	return j, nil
}
func uiEntries(root string, recovery bool) error { return uiEntriesObserved(root, recovery, nil) }
func uiEntriesObserved(root string, recovery bool, enumerated func()) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if enumerated != nil {
		enumerated()
	}
	for _, e := range entries {
		allowed := e.Name() == "owner.lock" || e.Name() == "journal.json" || e.Name() == "arm" || e.Name() == "finish" || e.Name() == "arm.tmp" || e.Name() == "finish.tmp" || (recovery && e.Name() == "journal.tmp")
		if !allowed {
			return errors.New("unexpected state entry")
		}
		if _, err = privateUIFile(filepath.Join(root, e.Name())); err != nil {
			if (e.Name() == "arm.tmp" || e.Name() == "finish.tmp") && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
	}
	return nil
}

// UISignal only creates a noncredential arm/finish marker for the exact journal.
// Duplicate markers never reset the helper's in-memory monotonic deadline.
func UISignal(c UIWatchConfig, kind string) error {
	if kind != "arm" && kind != "finish" {
		return errors.New("unknown signal")
	}
	root, digest, err := uiConfig(c)
	if err != nil {
		return err
	}
	j, err := uiJournal(root, digest, c.Identity)
	if err != nil {
		return err
	}
	if kind == "arm" && j.Phase != "PREPARED_STOPPED" {
		return errors.New("arm requires prepared stopped journal")
	}
	if j.Domain.Clean {
		return errors.New("fixture already clean")
	}
	return uiPublishSignal(root, kind, uiSignal{digest, c.Identity}, nil)
}
func uiPublishSignal(root, kind string, signal uiSignal, prepared func() error) error {
	final := filepath.Join(root, kind)
	temp := final + ".tmp"
	if _, e := os.Lstat(final); !errors.Is(e, os.ErrNotExist) {
		if e == nil {
			return os.ErrExist
		}
		return e
	}
	// The exclusive temp serializes signal publishers. Only this helper publishes
	// the final marker, atomically after complete write+fsync; it never rewrites it.
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	b, _ := json.Marshal(signal)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if prepared != nil {
		if err = prepared(); err != nil {
			return err
		}
	}
	if _, e := os.Lstat(final); !errors.Is(e, os.ErrNotExist) {
		if e == nil {
			return os.ErrExist
		}
		return e
	}
	if err = os.Rename(temp, final); err != nil {
		return err
	}
	return uiSync(root)
}

func uiReadSignal(root, kind, digest string, id Identity) (bool, error) {
	raw, err := privateUIFile(filepath.Join(root, kind))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var s uiSignal
	if err = strictUIJSON(raw, &s); err != nil {
		return false, err
	}
	if s.ConfigSHA256 != digest || s.Identity != id {
		return false, errors.New("signal identity mismatch")
	}
	return true, nil
}
func uiCheck(ctx context.Context, d Driver, j UIJournal) (string, error) {
	raw, err := d.XML(ctx, j.Domain.Identity.UUID)
	if err != nil {
		return "", err
	}
	if err = owned(raw, j.Domain.Identity); err != nil {
		return "", err
	}
	if j.Domain.Fingerprint != "" && hash(raw) != j.Domain.Fingerprint {
		return "", errors.New("fixture XML fingerprint changed")
	}
	return raw, nil
}

// uiCleanup never adopts a new fingerprint except the strict original/normalized
// pending define profile; it persists that pin before any destructive operation.
func uiCleanup(ctx context.Context, d Driver, root string, j *UIJournal, stoppedOnly bool) error {
	raw, err := uiCheck(ctx, d, *j)
	if errors.Is(err, ErrAbsent) {
		if _, e := d.XML(ctx, j.Domain.Identity.Name); !errors.Is(e, ErrAbsent) {
			return errors.New("name absence unverified")
		}
		j.Domain.Clean = true
		j.Domain.Pending = false
		j.Phase = "CLEAN"
		return uiSave(root, *j)
	}
	if err != nil {
		return err
	}
	if j.Domain.Clean {
		return errors.New("clean journal cannot authorize retained domain")
	}
	if j.Domain.Fingerprint == "" {
		next := *j
		next.Domain.Fingerprint = hash(raw)
		next.Domain.Pending = false
		if err = uiSave(root, next); err != nil {
			return err
		}
		*j = next
	}
	state, err := d.State(ctx, j.Domain.Identity.UUID)
	if err != nil {
		return err
	}
	if state != "running" && state != "shut off" {
		return errors.New("ambiguous native state; mutation refused")
	}
	if state == "running" {
		if stoppedOnly {
			return errors.New("routine stopped cleanup refuses running fixture")
		}
		j.Intervention = true
		j.Result = "FAIL_WATCHDOG_INTERVENED"
		if err = uiSave(root, *j); err != nil {
			return err
		}
		if _, err = uiCheck(ctx, d, *j); err != nil {
			return err
		}
		if err = d.Destroy(ctx, j.Domain.Identity.UUID); err != nil {
			return err
		}
	}
	if _, err = uiCheck(ctx, d, *j); err != nil {
		return err
	}
	state, err = d.State(ctx, j.Domain.Identity.UUID)
	if err != nil || state != "shut off" {
		return errors.New("shutoff not verified before undefine")
	}
	if _, err = uiCheck(ctx, d, *j); err != nil {
		return err
	}
	if err = d.Undefine(ctx, j.Domain.Identity.UUID); err != nil {
		return err
	}
	for _, target := range []string{j.Domain.Identity.UUID, j.Domain.Identity.Name} {
		if _, err = d.XML(ctx, target); !errors.Is(err, ErrAbsent) {
			return errors.New("complete fixture absence not verified")
		}
	}
	j.Domain.Clean = true
	j.Domain.Pending = false
	j.Phase = "CLEAN"
	return uiSave(root, *j)
}

// RunUIWatchdog is opt-in via the dedicated command. It has no endpoint, signing
// secret, telemetry, agent configuration or native Start call.
func RunUIWatchdog(ctx context.Context, c UIWatchConfig, d Driver, recovery bool, ready func(UIJournal)) error {
	return runUIWatchdog(ctx, c, d, recovery, ready, uiBudgets)
}
func runUIWatchdog(ctx context.Context, c UIWatchConfig, d Driver, recovery bool, ready func(UIJournal), timing uiTiming) (result error) {
	root, digest, err := uiConfig(c)
	if err != nil {
		return err
	}
	if !recovery {
		entries, e := os.ReadDir(root)
		if e != nil {
			return e
		}
		if len(entries) != 0 {
			return errors.New("fresh empty private state required")
		}
	}
	lock, err := uiLock(root, recovery)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = uiEntries(root, recovery); err != nil {
		return err
	}
	if recovery {
		j, err := uiJournal(root, digest, c.Identity)
		if err != nil {
			return err
		}
		// A valid committed journal is required before removing this exact incomplete
		// journal update. Never use its contents to invent ownership or reset a pin.
		if _, e := os.Lstat(filepath.Join(root, "journal.tmp")); e == nil {
			if e = os.Remove(filepath.Join(root, "journal.tmp")); e != nil {
				return e
			}
			if e = uiSync(root); e != nil {
				return e
			}
		}
		cleanup, cancel := context.WithTimeout(context.Background(), CleanupBudget)
		defer cancel()
		j.Result = "FAIL_RECOVERY_REQUIRED"
		return uiCleanup(cleanup, d, root, &j, false)
	}
	prepared := time.Now()
	prepareCtx, cancel := context.WithDeadline(ctx, prepared.Add(timing.prepare))
	defer cancel()
	j := UIJournal{Version: 1, ConfigSHA256: digest, Domain: Journal{Identity: c.Identity, Pending: true}, Phase: "DEFINING", PreparedAt: prepared, Result: "NOT_COMPLETE"}
	// Register cleanup only after durable ownership exists. Uncertain define
	// completion still uses this exact journal; no global inventory cleanup.
	for _, target := range []string{c.Identity.UUID, c.Identity.Name} {
		if _, err = d.XML(prepareCtx, target); !errors.Is(err, ErrAbsent) {
			return errors.New("fixture identity absence not verified")
		}
	}
	guardRefused := false
	var committed []byte
	save := func(next UIJournal) error {
		if e := uiSave(root, next); e != nil {
			return e
		}
		committed, _ = json.Marshal(next)
		return nil
	}
	if err = save(j); err != nil {
		return err
	}
	defer func() {
		if guardRefused {
			result = errors.Join(result, errors.New("ownership/state guard refused; fixture retained for review"))
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), CleanupBudget)
		defer stop()
		// Reread committed journal before destructive work; malformed or changed
		// ownership/fingerprint never falls back to in-memory authority.
		disk, e := uiJournal(root, digest, c.Identity)
		if e != nil {
			result = errors.Join(result, e)
			return
		}
		actual, _ := json.Marshal(disk)
		if !bytes.Equal(actual, committed) {
			result = errors.Join(result, errors.New("committed journal changed"))
			return
		}
		if e = uiEntries(root, false); e != nil {
			result = errors.Join(result, e)
			return
		}

		result = errors.Join(result, uiCleanup(cleanup, d, root, &j, j.Result == "PASS_NATIVE_OBSERVATION"))
		if j.Intervention && result == nil {
			result = errors.New("watchdog intervention: UI result failed")
		}
	}()
	if err = d.Define(prepareCtx, XML(c.Identity)); err != nil {
		return err
	}
	raw, err := uiCheck(prepareCtx, d, j)
	if err != nil {
		guardRefused = !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)
		return err
	}
	state, err := d.State(prepareCtx, c.Identity.UUID)
	if err != nil || state != "shut off" {
		guardRefused = state != "running" && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)
		return errors.New("prepared guest not shut off")
	}
	next := j
	next.Domain.Fingerprint = hash(raw)
	next.Domain.Pending = false
	next.Phase = "PREPARED_STOPPED"
	if err = save(next); err != nil {
		return err
	}
	j = next
	if ready != nil {
		ready(j)
	}
	ticker := time.NewTicker(timing.poll)
	defer ticker.Stop()
	active := prepareCtx
	for {
		select {
		case <-active.Done():
			j.Result = "FAIL_DEADLINE_OR_CANCEL"
			return active.Err()
		case <-ticker.C:
		}
		if err = uiEntries(root, false); err != nil {
			guardRefused = true
			return err
		}
		// Detect journal corruption/replacement before observation or signal handling.
		disk, err := uiJournal(root, digest, c.Identity)
		if err != nil {
			guardRefused = true
			return err
		}
		a, _ := json.Marshal(j)
		b, _ := json.Marshal(disk)
		if !bytes.Equal(a, b) {
			guardRefused = true
			return errors.New("journal changed externally")
		}
		finish, err := uiReadSignal(root, "finish", digest, c.Identity)
		if err != nil {
			return err
		}
		if finish {
			j.Result = "FAIL_OPERATOR_FINISH"
			return errors.New("UI sequence finished without successful observation")
		}
		if _, err = uiCheck(active, d, j); err != nil {
			guardRefused = !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)
			return err
		}
		state, err = d.State(active, c.Identity.UUID)
		if err != nil {
			guardRefused = !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)
			return err
		}
		if state != "running" && state != "shut off" {
			guardRefused = true
			return errors.New("ambiguous fixture state")
		}
		if j.Phase == "PREPARED_STOPPED" {
			if state != "shut off" {
				j.Result = "FAIL_STARTED_BEFORE_ARM"
				return errors.New("fixture started before arm")
			}
			armed, err := uiReadSignal(root, "arm", digest, c.Identity)
			if err != nil {
				return err
			}
			if !armed {
				continue
			}
			now := time.Now()
			next = j
			next.Phase = "ARMED"
			next.ArmedAt = now
			next.ArmedDeadline = now.Add(45 * time.Second)
			// The runtime deadline retains the monotonic clock and starts BEFORE fsync
			// and acknowledgment. No signal, retry, poll or observation can extend it.
			armedCtx, stopArmed := context.WithDeadline(ctx, now.Add(timing.armed))
			defer stopArmed()
			active = armedCtx
			if err = save(next); err != nil {
				return err
			}
			j = next
			if active.Err() != nil {
				return active.Err()
			}
			if ready != nil {
				ready(j)
			}
			continue
		}
		if j.Phase == "ARMED" && state == "running" {
			next = j
			next.Phase = "RUNNING_OBSERVED"
			next.RunningAt = time.Now()
			if err = save(next); err != nil {
				return err
			}
			j = next
			if ready != nil {
				ready(j)
			}
		} else if j.Phase == "RUNNING_OBSERVED" && state == "shut off" {
			next = j
			next.Phase = "STOPPED_OBSERVED"
			next.StoppedAt = time.Now()
			next.Result = "PASS_NATIVE_OBSERVATION"
			if err = save(next); err != nil {
				return err
			}
			j = next
			if ready != nil {
				ready(j)
			}
			return nil
		}
	}
}
