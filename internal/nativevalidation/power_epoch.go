package nativevalidation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	"kvm-center/internal/nativework"
)

var ErrPowerRecovery = errors.New("domain power operation requires retained-evidence recovery")
var ErrPowerProfile = errors.New("native power profile not qualified; installed hook and handler review plus native acceptance required")

type PowerRequest struct{ ID, Attempt, Action string }
type PowerResult struct {
	Epoch    int
	Status   string
	Replayed bool
}
type powerOperation struct {
	Request     PowerRequest
	Epoch       int
	Phases      []string
	Observation string
}
type powerHistory struct {
	Version                                     int
	Identity                                    Identity
	ExecutionSHA256, ProfileSHA256, Predecessor string
	PriorRevision                               int64
	Adopted                                     bool
	Operations                                  []powerOperation
}
type powerDriver interface {
	Driver
	PowerPersistent(context.Context, string) error
}

// PowerCoordinator owns one application domain scope. No currently installed
// native profile is qualified: its public constructor remains execution-gated.
// The finite state machine is exercised with synthetic drivers/subprocesses.
// History is logically append-only; replacement files preserve every event.
type PowerCoordinator struct {
	v                  Virsh
	d                  powerDriver
	previous           *nativework.Journal
	id                 Identity
	profile, execution string
	qualified          bool
	// Test-only failure injection after a durable phase; never execution authority.
	afterSave func(string) error
}

func NewPowerCoordinator(v Virsh, previous *nativework.Journal, id Identity, profileSHA256 string) (*PowerCoordinator, error) {
	if previous == nil || v.mutation != nil || v.power != nil {
		return nil, errors.New("unbound canonical driver and predecessor required")
	}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	if !ownerPattern.MatchString(profileSHA256) {
		return nil, errors.New("exact profile digest required")
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(struct {
		Binary, Socket, Home string
		Identity             Identity
	}{v.Binary, v.Socket, v.Home, id})
	return &PowerCoordinator{v: v, d: v, previous: previous, id: id, profile: profileSHA256, execution: hash(string(raw))}, nil
}
func validPowerRequest(r PowerRequest) bool {
	if !utf8.ValidString(r.ID) || !utf8.ValidString(r.Attempt) || r.ID == "" || r.Attempt == "" || len(r.ID) > 128 || len(r.Attempt) > 128 || strings.TrimSpace(r.ID) != r.ID || strings.TrimSpace(r.Attempt) != r.Attempt || strings.ContainsAny(r.ID+r.Attempt, "\x00\r\n") {
		return false
	}
	switch r.Action {
	case "define", "start", "destroy", "undefine":
		return true
	}
	return false
}
func validatePowerHistory(h powerHistory) error {
	bad := errors.New("unsupported or inconsistent power epoch history")
	if h.Version != 1 || h.Identity.Validate() != nil || !ownerPattern.MatchString(h.ExecutionSHA256) || !ownerPattern.MatchString(h.ProfileSHA256) || !ownerPattern.MatchString(h.Predecessor) || h.PriorRevision < 0 || h.Operations == nil || len(h.Operations) > 256 {
		return bad
	}
	if !h.Adopted && len(h.Operations) != 0 {
		return bad
	}
	ids, attempts := map[string]bool{}, map[string]bool{}
	for i, o := range h.Operations {
		if o.Epoch != i+1 || !validPowerRequest(o.Request) || ids[o.Request.ID] || attempts[o.Request.Attempt] || len(o.Phases) < 1 || len(o.Phases) > 5 {
			return bad
		}
		ids[o.Request.ID] = true
		attempts[o.Request.Attempt] = true
		if len(o.Phases) == 2 && o.Phases[0] == "registered" && o.Phases[1] == "aborted" {
			if o.Observation != "" {
				return bad
			}
			continue
		}
		want := []string{"registered", "invocation_possible", "native_ack", "observed", "completed"}
		for n, s := range o.Phases {
			if n == 3 && s == "recovery_observed" {
				continue
			}
			if s != want[n] {
				return bad
			}
		}
		if len(o.Phases) < 4 && o.Observation != "" {
			return bad
		}
		if len(o.Phases) >= 4 && !ownerPattern.MatchString(o.Observation) {
			return bad
		}
		if i < len(h.Operations)-1 && len(o.Phases) != 5 {
			return bad
		}
	}
	return nil
}
func (p *PowerCoordinator) read(root string) (powerHistory, error) {
	var h powerHistory
	raw, err := ReadPrivate(filepath.Join(root, "epoch.json"))
	if err != nil {
		return h, err
	}
	if err = strictPowerJSON(raw, &h); err != nil {
		return h, err
	}
	if err = validatePowerHistory(h); err != nil {
		return h, err
	}
	if h.Identity != p.id || h.ExecutionSHA256 != p.execution || h.ProfileSHA256 != p.profile || h.Predecessor != p.previous.ScopeID() {
		return h, errors.New("power epoch immutable binding mismatch")
	}
	return h, nil
}
func (p *PowerCoordinator) save(root string, h powerHistory, phase string) error {
	if err := validatePowerHistory(h); err != nil {
		return err
	}
	raw, err := json.Marshal(h)
	if err != nil || len(raw) > 1<<20 {
		return errors.New("power history capacity exceeded; records retained")
	}
	tmp := filepath.Join(root, "epoch.tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(root, "epoch.json")); err != nil {
		return err
	}
	if err = uiSync(root); err != nil {
		return err
	}
	if p.afterSave != nil {
		return p.afterSave(phase)
	}
	return nil
}
func (p *PowerCoordinator) predecessor(ctx context.Context, h powerHistory) error {
	e, err := p.previous.Evidence(ctx, nativework.Target{UUID: p.id.UUID, Name: p.id.Name})
	if err != nil {
		return err
	}
	if !e.TargetAdmissionClosed || e.Revision != h.PriorRevision+1 || e.RegisteredWritersUnresolved != 0 {
		return errors.New("sealed predecessor evidence changed")
	}
	return nil
}
func (p *PowerCoordinator) history(ctx context.Context, root string) (powerHistory, error) {
	if err := bindPowerPredecessor(root, p.previous.ScopeID()); err != nil {
		return powerHistory{}, err
	}
	// An incomplete replacement is never treated as fresh authority. A separately
	// reviewed repair may inspect it; this implementation preserves it and refuses.
	if _, err := os.Lstat(filepath.Join(root, "epoch.tmp")); !errors.Is(err, os.ErrNotExist) {
		return powerHistory{}, ErrPowerRecovery
	}
	h, err := p.read(root)
	if errors.Is(err, os.ErrNotExist) {
		if _, markerErr := os.Lstat(filepath.Join(root, "scope.json")); !errors.Is(markerErr, os.ErrNotExist) {
			return h, ErrPowerRecovery
		}
		// Only absent native identity and a drained, previously open predecessor may
		// establish the first intent. A missing ledger after sealing cannot reset it.
		if err = p.absent(ctx); err != nil {
			return h, err
		}
		var rev int64
		rev, err = p.previous.Handoff(ctx, nativework.Target{UUID: p.id.UUID, Name: p.id.Name}, nil)
		if err != nil {
			return h, err
		}
		h = powerHistory{Version: 1, Identity: p.id, ExecutionSHA256: p.execution, ProfileSHA256: p.profile, Predecessor: p.previous.ScopeID(), PriorRevision: rev, Operations: []powerOperation{}}
		if err = p.save(root, h, "adoption_intent"); err != nil {
			return h, err
		}
	} else if err != nil {
		return h, err
	}
	if err = p.scopeMarker(root, h); err != nil {
		return h, err
	}
	if !h.Adopted {
		if _, err = p.previous.Handoff(ctx, nativework.Target{UUID: p.id.UUID, Name: p.id.Name}, &h.PriorRevision); err != nil {
			return h, err
		}
		h.Adopted = true
		if err = p.save(root, h, "adopted"); err != nil {
			return h, err
		}
	}
	return h, p.predecessor(ctx, h)
}
func (p *PowerCoordinator) absent(ctx context.Context) error {
	for _, target := range []string{p.id.UUID, p.id.Name} {
		if _, err := p.d.XML(ctx, target); !errors.Is(err, ErrAbsent) {
			return errors.New("complete exact fixture absence not verified")
		}
	}
	return nil
}
func (p *PowerCoordinator) ownedState(ctx context.Context) (string, string, error) {
	raw, err := p.d.XML(ctx, p.id.UUID)
	if err != nil {
		return "", "", err
	}
	if err = owned(raw, p.id); err != nil {
		return "", "", err
	}
	if err = p.d.PowerPersistent(ctx, p.id.UUID); err != nil {
		return "", "", err
	}
	state, err := p.d.State(ctx, p.id.UUID)
	if err != nil {
		return "", "", err
	}
	return state, hash(raw), nil
}
func (p *PowerCoordinator) precondition(ctx context.Context, action string) error {
	if action == "define" {
		return p.absent(ctx)
	}
	state, _, err := p.ownedState(ctx)
	if err != nil {
		return err
	}
	want := "shut off"
	if action == "destroy" {
		want = "running"
	}
	if state != want {
		return errors.New("power operation precondition mismatch")
	}
	return nil
}
func (p *PowerCoordinator) observe(ctx context.Context, action string) (string, error) {
	if action == "undefine" {
		if err := p.absent(ctx); err != nil {
			return "", err
		}
		return hash(p.id.UUID + "/" + p.id.Name + "/absent"), nil
	}
	state, xmlHash, err := p.ownedState(ctx)
	if err != nil {
		return "", err
	}
	want := "shut off"
	if action == "start" {
		want = "running"
	}
	if state != want {
		return "", errors.New("power operation postcondition mismatch")
	}
	return hash(action + "/" + state + "/" + xmlHash), nil
}
func (p *PowerCoordinator) invoke(ctx context.Context, r PowerRequest) error {
	d := p.d
	if _, ok := d.(Virsh); ok {
		v := p.v
		v.power = &powerInvocation{v: p.v, id: p.id, action: r.Action}
		d = v
	}
	switch r.Action {
	case "define":
		return d.Define(ctx, XML(p.id))
	case "start":
		return d.Start(ctx, p.id.UUID)
	case "destroy":
		return d.Destroy(ctx, p.id.UUID)
	case "undefine":
		return d.Undefine(ctx, p.id.UUID)
	}
	return errors.New("unsupported power action")
}

// Execute serializes admission, native invocation, acknowledgement and observation
// under the fixed domain lock. Native-profile qualification cannot be supplied as
// a caller boolean. It is intentionally absent until installed-hook review passes.
func (p *PowerCoordinator) Execute(ctx context.Context, r PowerRequest) (PowerResult, error) {
	if !p.qualified {
		return PowerResult{}, ErrPowerProfile
	}
	if !validPowerRequest(r) {
		return PowerResult{}, errors.New("invalid power request")
	}
	root, err := powerScope(p.v, p.id)
	if err != nil {
		return PowerResult{}, err
	}
	lock, err := powerLock(root)
	if err != nil {
		return PowerResult{}, err
	}
	defer lock.Close()
	h, err := p.history(ctx, root)
	if err != nil {
		return PowerResult{}, err
	}
	for i := range h.Operations {
		o := &h.Operations[i]
		if o.Request.ID == r.ID {
			if o.Request != r {
				return PowerResult{}, errors.New("power request binding conflict")
			}
			return p.finish(ctx, root, &h, i, true)
		}
		if o.Request.Attempt == r.Attempt {
			return PowerResult{}, errors.New("attempt identity already retained")
		}
	}
	if len(h.Operations) > 0 {
		last := h.Operations[len(h.Operations)-1]
		phase := last.Phases[len(last.Phases)-1]
		if phase != "completed" && phase != "aborted" {
			return PowerResult{Epoch: last.Epoch, Status: "recovery_required"}, ErrPowerRecovery
		}
	}
	if len(h.Operations) >= 256 {
		return PowerResult{}, errors.New("power request capacity reached; history retained")
	}
	if err = p.precondition(ctx, r.Action); err != nil {
		return PowerResult{}, err
	}
	o := powerOperation{Request: r, Epoch: len(h.Operations) + 1, Phases: []string{"registered"}}
	h.Operations = append(h.Operations, o)
	i := len(h.Operations) - 1
	if err = p.save(root, h, "registered"); err != nil {
		return PowerResult{}, err
	}
	h.Operations[i].Phases = append(h.Operations[i].Phases, "invocation_possible")
	if err = p.save(root, h, "invocation_possible"); err != nil {
		return PowerResult{}, err
	}
	if err = p.invoke(ctx, r); err != nil {
		return PowerResult{Epoch: o.Epoch, Status: "recovery_required"}, errors.Join(ErrPowerRecovery, err)
	}
	h.Operations[i].Phases = append(h.Operations[i].Phases, "native_ack")
	if err = p.save(root, h, "native_ack"); err != nil {
		return PowerResult{}, err
	}
	return p.finish(ctx, root, &h, i, false)
}
func (p *PowerCoordinator) finish(ctx context.Context, root string, h *powerHistory, i int, recovery bool) (PowerResult, error) {
	o := &h.Operations[i]
	result := PowerResult{Epoch: o.Epoch, Replayed: recovery, Status: "recovery_required"}
	phase := o.Phases[len(o.Phases)-1]
	if phase == "completed" || phase == "aborted" {
		result.Status = phase
		return result, nil
	}
	if phase == "registered" {
		o.Phases = append(o.Phases, "aborted")
		if err := p.save(root, *h, "aborted"); err != nil {
			return result, err
		}
		result.Status = "aborted"
		return result, nil
	}
	if phase == "invocation_possible" {
		result.Status = "recovery_required"
		return result, ErrPowerRecovery
	}
	// Recovery never invokes native mutation. A durable acknowledgement is the
	// prerequisite; current state alone cannot manufacture one.
	observation, err := p.observe(ctx, o.Request.Action)
	if err != nil {
		return result, errors.Join(ErrPowerRecovery, err)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if phase == "native_ack" {
		stage := "observed"
		if recovery {
			stage = "recovery_observed"
		}
		o.Phases = append(o.Phases, stage)
		o.Observation = observation
		if err = p.save(root, *h, stage); err != nil {
			return result, err
		}
	} else if observation != o.Observation {
		return result, errors.New("retained postcondition changed before completion")
	}
	if err = p.predecessor(ctx, *h); err != nil {
		return result, err
	}
	o.Phases = append(o.Phases, "completed")
	if err = p.save(root, *h, "completed"); err != nil {
		return result, err
	}
	result.Status = "completed"
	return result, nil
}

// powerInvocation is constructed only while Execute holds the canonical domain
// lock and has persisted invocation_possible. It cannot be forged by API callers.
type powerInvocation struct {
	mu     sync.Mutex
	used   bool
	v      Virsh
	id     Identity
	action string
}

func (p *powerInvocation) run(v Virsh, cmd *exec.Cmd, input string, args []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.used {
		return errors.New("native invocation already consumed")
	}
	p.used = true
	if v.Binary != p.v.Binary || v.Socket != p.v.Socket || v.Home != p.v.Home || len(args) != 2 || args[0] != p.action {
		return errors.New("native invocation binding mismatch")
	}
	if p.action == "define" {
		if input != XML(p.id) || args[1] != "/dev/stdin" {
			return errors.New("definition binding mismatch")
		}
	} else if input != "" || args[1] != p.id.UUID {
		return errors.New("native target binding mismatch")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Wait()
}
func (v Virsh) PowerPersistent(ctx context.Context, id string) error {
	raw, err := v.run(ctx, "", "dominfo", id)
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		k, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		switch k {
		case "Persistent", "Managed save", "Autostart":
			if _, exists := values[k]; exists {
				return errors.New("ambiguous domain flags")
			}
			values[k] = strings.TrimSpace(val)
		}
	}
	if values["Persistent"] != "yes" || values["Managed save"] != "no" || values["Autostart"] != "disable" {
		return fmt.Errorf("unsupported persistent-domain power profile")
	}
	return nil
}

func (p *PowerCoordinator) scopeMarker(root string, h powerHistory) error {
	path := filepath.Join(root, "scope.json")
	raw, err := ReadPrivate(path)
	expected := h
	expected.Adopted = false
	expected.Operations = []powerOperation{}
	if errors.Is(err, os.ErrNotExist) {
		if h.Adopted {
			return ErrPowerRecovery
		}
		raw, _ = json.Marshal(expected)
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		if _, e = f.Write(raw); e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return e
		}
		return uiSync(root)
	}
	if err != nil {
		return err
	}
	var actual powerHistory
	if err = strictPowerJSON(raw, &actual); err != nil {
		return err
	}
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(expected)
	if string(a) != string(b) {
		return errors.New("canonical power scope predecessor mismatch")
	}
	return nil
}

// Require the complete case-sensitive schema at every level. Null, omitted and
// aliased fields must never collapse retained operations into zero values.
func strictPowerJSON(raw []byte, out *powerHistory) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid power history encoding")
	}
	if err := strictUIJSON(raw, out); err != nil {
		return err
	}
	return powerJSONShape(raw, reflect.TypeOf(*out))
}
func powerJSONShape(raw []byte, typ reflect.Type) error {
	bad := errors.New("noncanonical power history fields")
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return bad
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || len(fields) != typ.NumField() {
			return bad
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			value, ok := fields[f.Name]
			if !ok {
				return bad
			}
			if err := powerJSONShape(value, f.Type); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return bad
		}
		for _, value := range values {
			if err := powerJSONShape(value, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// The first legacy or managed owner pins the predecessor before registration.
// A failed or partial creation remains a refusal, never fresh authority.
func bindPowerPredecessor(root, scope string) error {
	path := filepath.Join(root, "predecessor")
	raw, err := ReadPrivate(path)
	if err == nil {
		if string(raw) != scope {
			return errors.New("canonical predecessor mismatch")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(scope); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return uiSync(root)
}
