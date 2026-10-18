package nativevalidation

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/nativework"
)

var ErrMutationTrackingRequired = errors.New("native mutation requires a bound single-use writer tracker")
var ErrNativeEffectsUnresolved = errors.New("native daemon effects remain unresolved; retry and cleanup are not authorized")

// ProcessReceipt describes only the exact direct subprocess retained by this
// invocation. Reaped is set only after Wait returns with a ProcessState. It says
// nothing about descendants, libvirt jobs, guest state or writer cessation. It
// is deliberately not a durable completion receipt and is lost on restart.
type ProcessReceipt struct {
	Writer                nativework.Identity `json:"writer"`
	Registered            bool                `json:"registered"`
	Started               bool                `json:"started"`
	Reaped                bool                `json:"direct_process_reaped"`
	ExitCode              int                 `json:"exit_code"`
	NativeEffectsResolved bool                `json:"native_effects_resolved"`
}

type VirshMutation struct {
	mu                       sync.Mutex
	journal                  *nativework.Journal
	binary, socket, home     string
	identity                 Identity
	request, attempt, action string
	used                     bool
	receipt                  ProcessReceipt
}

// TrackVirshMutation binds one invocation of one exact mutation. The caller must
// already own the fixture and exclude external changes to its private state and
// executable. This is not a qualified daemon observer or a cleanup capability.
// Both root and child remain unresolved, including after successful process exit.
func TrackVirshMutation(v Virsh, j *nativework.Journal, id Identity, request, attempt, action string) (Virsh, *VirshMutation, error) {
	if j == nil || v.mutation != nil {
		return Virsh{}, nil, ErrMutationTrackingRequired
	}
	if err := id.Validate(); err != nil {
		return Virsh{}, nil, err
	}
	switch action {
	case "define", "start", "destroy", "undefine":
	default:
		return Virsh{}, nil, errors.New("unsupported tracked mutation")
	}
	if err := v.Validate(); err != nil {
		return Virsh{}, nil, err
	}
	m := &VirshMutation{journal: j, binary: v.Binary, socket: v.Socket, home: v.Home, identity: id, request: request, attempt: attempt, action: action, receipt: ProcessReceipt{ExitCode: -1}}
	v.mutation = m
	return v, m, nil
}

func (m *VirshMutation) Receipt() ProcessReceipt { m.mu.Lock(); defer m.mu.Unlock(); return m.receipt }

type unresolvedNativeObserver struct{}

func (unresolvedNativeObserver) AwaitStopped(context.Context, nativework.Identity) error {
	return ErrNativeEffectsUnresolved
}

// NativeMutationReadiness prevents workflows with automatic destructive cleanup
// from activating. Direct invocation tracking is not sufficient to run them.
func (v Virsh) NativeMutationReadiness() error { return ErrNativeEffectsUnresolved }

func (m *VirshMutation) run(ctx context.Context, v Virsh, cmd *exec.Cmd, input string, args []string) error {
	m.mu.Lock()
	if m.used {
		m.mu.Unlock()
		return nativework.ErrReplay
	}
	// Consume before any validation or persistence attempt. Never reset on error.
	m.used = true
	if v.Binary != m.binary || v.Socket != m.socket || v.Home != m.home || len(args) != 2 || args[0] != m.action || (m.action == "define" && (args[1] != "/dev/stdin" || input != XML(m.identity))) || (m.action != "define" && (args[1] != m.identity.UUID || input != "")) {
		m.mu.Unlock()
		return errors.New("tracked mutation binding mismatch")
	}
	m.mu.Unlock()
	raw, _ := json.Marshal(struct {
		Binary, Socket, Home, Input string
		Identity                    Identity
		Args                        []string
	}{v.Binary, v.Socket, v.Home, input, m.identity, args})
	req := nativework.Request{ID: m.request, AttemptID: m.attempt, Action: "VIRSH_" + strings.ToUpper(m.action), ParamsSHA256: hash(string(raw)), Target: nativework.Target{UUID: m.identity.UUID, Name: m.identity.Name}}
	root, err := m.journal.Begin(ctx, req, unresolvedNativeObserver{})
	if err != nil {
		return err
	}
	child, err := m.journal.Child(ctx, root, "virsh-direct-process", unresolvedNativeObserver{})
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.receipt.Writer = child.Identity()
	m.receipt.Registered = true
	m.mu.Unlock()
	// No native call or subprocess may precede the two durable registrations.
	err = cmd.Start()
	if err == nil {
		m.mu.Lock()
		m.receipt.Started = true
		m.mu.Unlock()
		err = cmd.Wait() // Exactly once, even after cancellation or an output error.
		m.mu.Lock()
		if cmd.ProcessState != nil {
			m.receipt.Reaped = true
			m.receipt.ExitCode = cmd.ProcessState.ExitCode()
		}
		m.mu.Unlock()
	}
	if ctx.Err() != nil {
		recordCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = errors.Join(err, m.journal.Cancel(recordCtx, root), m.journal.Cancel(recordCtx, child))
		cancel()
	}
	// Never Complete either lease. Process exit is not native effects cessation.
	return errors.Join(ErrNativeEffectsUnresolved, err)
}

func nativeMutationReadiness(d Driver) error {
	if readiness, ok := d.(interface{ NativeMutationReadiness() error }); ok {
		return readiness.NativeMutationReadiness()
	}
	return ErrNativeEffectsUnresolved
}
