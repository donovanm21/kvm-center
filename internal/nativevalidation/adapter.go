// Package nativevalidation implements a deliberately separate disposable-domain
// adapter. It never starts the production agent; absence checks read only identifiers.
package nativevalidation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/agentauth"
	"kvm-center/internal/models"
)

var ErrAbsent = errors.New("domain absent")
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var namePattern = regexp.MustCompile(`^kvmc-validation-[a-z0-9-]{1,32}$`)
var ownerPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Identity struct{ UUID, Name, Owner string }

func (id Identity) Validate() error {
	if !uuidPattern.MatchString(id.UUID) || !namePattern.MatchString(id.Name) || !ownerPattern.MatchString(id.Owner) {
		return errors.New("explicit validation name, UUIDv4 and owner marker required")
	}
	return nil
}

// Driver operates on one exact name/UUID. Implementations must distinguish
// absence from connection/permission errors. Virsh may verify a failed XML
// lookup against a bounded local identifier inventory; never invoke a shell.
type Driver interface {
	XML(context.Context, string) (string, error)
	Define(context.Context, string) error
	State(context.Context, string) (string, error)
	Start(context.Context, string) error
	Destroy(context.Context, string) error
	Undefine(context.Context, string) error
}
type Journal struct {
	Identity    Identity
	Fingerprint string
	Pending     bool
	Clean       bool
	Requests    []RequestRecord
}
type RequestRecord struct{ ID, Action, UUID, Outcome string }
type Save func(Journal) error

type Adapter struct {
	mu       sync.Mutex
	driver   Driver
	journal  Journal
	save     Save
	secret   string
	host     string
	closed   bool
	fatal    error
	outcomes map[string]cached
}
type cached struct {
	Request  models.CommandRequest
	Response models.CommandResponse
}

func XML(id Identity) string {
	// All interpolated fields have a restricted alphabet; there are no disks,
	// NICs, graphics, host devices, guest agents or host file mappings.
	return fmt.Sprintf(`<domain type="kvm"><name>%s</name><uuid>%s</uuid><metadata><ownership xmlns="urn:kvm-center:validation:1">%s</ownership></metadata><memory unit="KiB">262144</memory><currentMemory unit="KiB">262144</currentMemory><vcpu>1</vcpu><os><type arch="x86_64">hvm</type><boot dev="hd"/></os><on_poweroff>destroy</on_poweroff><on_reboot>destroy</on_reboot><on_crash>destroy</on_crash><devices><controller type="usb" model="none"/><memballoon model="none"/></devices></domain>`, id.Name, id.UUID, id.Owner)
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// Prepare refuses any existing UUID OR name, including a matching marker.
// Recover never adopts by name; it requires the private journal created before
// definition. The marker handles uncertain define completion before hash save.
func Prepare(ctx context.Context, d Driver, id Identity, secret string, save Save) (*Adapter, error) {
	if err := nativeMutationReadiness(d); err != nil {
		return nil, err
	}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	if len(secret) < 32 {
		return nil, errors.New("synthetic signing secret must contain at least 32 bytes")
	}
	for _, target := range []string{id.UUID, id.Name} {
		if _, err := d.XML(ctx, target); !errors.Is(err, ErrAbsent) {
			if err == nil {
				return nil, errors.New("domain identity collision")
			}
			return nil, err
		}
	}
	a := &Adapter{driver: d, journal: Journal{Identity: id, Pending: true}, save: save, secret: secret, host: id.Name, outcomes: map[string]cached{}}
	if err := save(a.journal); err != nil {
		return nil, err
	}
	// Return the adapter even on uncertain failure so the caller can run cleanup.
	if err := d.Define(ctx, XML(id)); err != nil {
		return a, err
	}
	raw, err := d.XML(ctx, id.UUID)
	if err != nil {
		return a, err
	}
	if err = owned(raw, id); err != nil {
		return a, err
	}
	a.journal.Fingerprint = hash(raw)
	a.journal.Pending = false
	if err = save(a.journal); err != nil {
		return a, err
	}
	return a, nil
}
func Recover(d Driver, j Journal, save Save) (*Adapter, error) {
	if err := nativeMutationReadiness(d); err != nil {
		return nil, err
	}
	if err := j.Identity.Validate(); err != nil {
		return nil, err
	}
	if j.Fingerprint == "" && !j.Pending && !j.Clean {
		return nil, errors.New("invalid journal")
	}
	return &Adapter{driver: d, journal: j, save: save, closed: true}, nil
}
func (a *Adapter) check(ctx context.Context) (string, error) {
	raw, err := a.driver.XML(ctx, a.journal.Identity.UUID)
	if err != nil {
		return "", err
	}
	if err = owned(raw, a.journal.Identity); err != nil {
		return "", err
	}
	if a.journal.Fingerprint != "" && hash(raw) != a.journal.Fingerprint {
		return "", errors.New("domain configuration changed; refusing mutation")
	}
	return raw, nil
}
func (a *Adapter) Cleanup(ctx context.Context) error { return a.cleanup(ctx, false) }

// CleanupStopped never starts or destroys a guest; recovery refuses a running guest.
func (a *Adapter) CleanupStopped(ctx context.Context) error { return a.cleanup(ctx, true) }

func stoppedRecoveryJournal(j Journal) error {
	if (j.Pending && j.Fingerprint != "") || len(j.Requests) != 0 || (j.Fingerprint == "" && !j.Pending && !j.Clean) || (j.Fingerprint != "" && !ownerPattern.MatchString(j.Fingerprint)) {
		return errors.New("unsupported stopped-only recovery journal")
	}
	return nil
}

func (a *Adapter) cleanup(ctx context.Context, stoppedOnly bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	if stoppedOnly {
		if err := stoppedRecoveryJournal(a.journal); err != nil {
			return err
		}
	}
	if _, err := a.check(ctx); errors.Is(err, ErrAbsent) {
		a.journal.Clean = true
		return a.save(a.journal)
	} else if err != nil {
		return err
	}
	if stoppedOnly && a.journal.Clean {
		return errors.New("clean journal cannot authorize a retained domain")
	}
	state, err := a.driver.State(ctx, a.journal.Identity.UUID)
	if err != nil {
		return err
	}
	if state != "shut off" {
		if stoppedOnly {
			return errors.New("stopped-only recovery requires verified shutoff; refusing mutation")
		}
		if _, err = a.check(ctx); err != nil {
			return err
		}
		if err = a.driver.Destroy(ctx, a.journal.Identity.UUID); err != nil {
			return err
		}
	}
	// Pin a fully validated interrupted definition before removal. Subsequent
	// reads must match these exact bytes, even when the original journal had
	// not yet recorded the post-define fingerprint.
	raw, err := a.check(ctx)
	if err != nil {
		return err
	}
	if a.journal.Fingerprint == "" {
		next := a.journal
		next.Fingerprint = hash(raw)
		next.Pending = false
		if err = a.save(next); err != nil {
			return err
		}
		a.journal = next
	}
	if _, err = a.check(ctx); err != nil {
		return err
	}
	state, err = a.driver.State(ctx, a.journal.Identity.UUID)
	if err != nil {
		return err
	}
	if state != "shut off" {
		return errors.New("cleanup requires verified shutoff")
	}
	if err = a.driver.Undefine(ctx, a.journal.Identity.UUID); err != nil {
		return err
	}
	if _, err = a.driver.XML(ctx, a.journal.Identity.UUID); !errors.Is(err, ErrAbsent) {
		return errors.New("cleanup could not verify domain absence")
	}
	a.journal.Clean = true
	return a.save(a.journal)
}
func (a *Adapter) Inventory(ctx context.Context) (models.TelemetryPayload, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inventory(ctx)
}
func (a *Adapter) inventory(ctx context.Context) (models.TelemetryPayload, error) {
	p := models.TelemetryPayload{HostID: a.journal.Identity.Name, IP: "127.0.0.1", CPUCount: 1, MemTotalMB: 256, AgentVersion: "validation-only", VMs: []models.VM{}, Storage: []models.StoragePool{}, Timestamp: time.Now()}
	if a.closed {
		return p, errors.New("validation closed")
	}
	if _, err := a.check(ctx); err != nil {
		return p, err
	}
	state, err := a.driver.State(ctx, a.journal.Identity.UUID)
	if err != nil {
		return p, err
	}
	var vmState models.VMState
	switch state {
	case "running":
		vmState = models.VMRunning
	case "shut off":
		vmState = models.VMStopped
	default:
		return p, errors.New("unexpected native domain state")
	}
	p.VMs = append(p.VMs, models.VM{UUID: a.journal.Identity.UUID, Name: a.journal.Identity.Name, HostID: p.HostID, State: vmState, CPUCount: 1, MemTotalMB: 256, Networks: []models.VMNetwork{}})
	return p, nil
}
func (a *Adapter) command(ctx context.Context, r models.CommandRequest) (resp models.CommandResponse, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() {
		if a.closed && err != nil {
			a.fatal = err
		}
	}()
	resp = models.CommandResponse{RequestID: r.RequestID}
	if a.closed {
		return resp, errors.New("validation closed")
	}
	if err := ctx.Err(); err != nil {
		return resp, err
	}
	if r.RequestID == "" || len(r.RequestID) > 128 || r.VMUUID != a.journal.Identity.UUID || r.VMName != a.journal.Identity.Name || len(r.Params) != 0 || (r.Action != "START" && r.Action != "FORCESTOP") {
		return resp, errors.New("only owned START/FORCESTOP with exact UUID/name and no parameters are allowed")
	}
	if _, err := a.check(ctx); err != nil {
		return resp, err
	}
	if c, ok := a.outcomes[r.RequestID]; ok {
		if c.Request.Action != r.Action || c.Request.VMUUID != r.VMUUID || c.Request.VMName != r.VMName {
			return resp, errors.New("request identity collision")
		}
		resp = c.Response
		resp.Replayed = true
		return resp, nil
	}
	if len(a.journal.Requests) >= 64 {
		return resp, errors.New("validation command budget exhausted")
	}
	if _, err := a.check(ctx); err != nil {
		return resp, err
	}
	state, err := a.driver.State(ctx, r.VMUUID)
	if err != nil {
		return resp, err
	}
	if (r.Action == "START" && state != "shut off") || (r.Action == "FORCESTOP" && state != "running") {
		return resp, errors.New("native state does not permit action")
	}
	a.journal.Requests = append(a.journal.Requests, RequestRecord{r.RequestID, r.Action, r.VMUUID, "pending"})
	if err = a.save(a.journal); err != nil {
		a.closed = true
		return resp, err
	}
	switch r.Action {
	case "START":
		if state != "shut off" {
			return resp, errors.New("START requires shutoff")
		}
		err = a.driver.Start(ctx, r.VMUUID)
	case "FORCESTOP":
		if state != "running" {
			return resp, errors.New("FORCESTOP requires running")
		}
		err = a.driver.Destroy(ctx, r.VMUUID)
	}
	if err == nil {
		var p models.TelemetryPayload
		p, err = a.inventory(ctx)
		if err == nil && ((r.Action == "START" && p.VMs[0].State != models.VMRunning) || (r.Action == "FORCESTOP" && p.VMs[0].State != models.VMStopped)) {
			err = errors.New("native state does not match command")
		}
	}
	if err != nil {
		a.closed = true
		return resp, errors.New("native operation uncertain; close runtime and perform ownership-checked cleanup")
	}
	a.journal.Requests[len(a.journal.Requests)-1].Outcome = "success"
	if err = a.save(a.journal); err != nil {
		a.closed = true
		return resp, err
	}
	resp.Success = true
	resp.Message = "owned validation action completed"
	a.outcomes[r.RequestID] = cached{r, resp}
	return resp, nil
}
func (a *Adapter) Handler(cancel context.CancelFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || !((r.URL.Path == "/command" && r.Method == http.MethodPost) || (r.URL.Path == "/validation/inventory" && r.Method == http.MethodGet)) {
			http.Error(w, "unsupported validation operation", http.StatusForbidden)
			return
		}
		if agentauth.Verify(a.secret, a.host, r.Method, r.URL.Path, r.Header.Get(agentauth.HeaderAuth), r.Header.Get(agentauth.HeaderExp), r.Header.Get(agentauth.HeaderNonce), time.Now()) != nil {
			http.Error(w, "signature required", http.StatusUnauthorized)
			return
		}
		ctx, stop := context.WithTimeout(r.Context(), 8*time.Second)
		defer stop()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/validation/inventory" {
			p, err := a.Inventory(ctx)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			_ = json.NewEncoder(w).Encode(p)
			return
		}
		var req models.CommandRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			http.Error(w, "invalid command", http.StatusBadRequest)
			return
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			http.Error(w, "invalid trailing command", http.StatusBadRequest)
			return
		}
		resp, err := a.command(ctx, req)
		if err != nil {
			a.mu.Lock()
			closed := a.closed
			a.mu.Unlock()
			if closed {
				cancel()
			}
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// HostID is synthetic and cannot be selected independently of the owned name.
func (a *Adapter) HostID() string { return strings.TrimSpace(a.journal.Identity.Name) }

func (a *Adapter) failure() error { a.mu.Lock(); defer a.mu.Unlock(); return a.fatal }
