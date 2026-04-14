package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/models"
)

// contextWithTimeout centralises the backend I/O deadline so every
// call site uses the same budget. Backend implementations should
// honour it via ctx.Done().
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// persistData is the on-disk JSON structure.
type persistData struct {
	Hosts      map[string]*models.Host             `json:"hosts"`
	VMs        map[string][]*models.VM             `json:"vms"`
	Storage    map[string][]*models.StoragePool    `json:"storage"`
	VMNotes    map[string]string                   `json:"vm_notes"`
	GuestData  map[string]*guestEntry              `json:"guest_data"`
	Migrations []*models.MigrationRecord           `json:"migrations,omitempty"`
	Users      map[string]*models.User             `json:"users,omitempty"`
	Groups     map[string]*models.Group            `json:"groups,omitempty"`
	Roles      map[string]*models.Role             `json:"roles,omitempty"`
	Sessions   map[string]*models.Session          `json:"sessions,omitempty"`
	IDPs       map[string]*models.IdentityProvider `json:"idps,omitempty"`
	Features   *models.Features                    `json:"features,omitempty"`
	Audit            []*models.AuditEvent     `json:"audit,omitempty"`
	AuditShipping    *models.AuditShipping    `json:"audit_shipping,omitempty"`
	SnapshotPolicies map[string]*models.SnapshotPolicy `json:"snapshot_policies,omitempty"`
	Notifications    *models.NotificationConfig        `json:"notifications,omitempty"`
	APITokens        map[string]*models.APIToken       `json:"api_tokens,omitempty"`
	HypervisorCerts  map[string]*models.HypervisorCert `json:"hypervisor_certs,omitempty"`
	// ConsoleSecret is the HMAC-SHA256 key the server uses to mint
	// per-session VM console tokens (see console_token.go). Generated
	// on first use; persisted so agents that cached it don't need a
	// re-fetch after a server restart.
	ConsoleSecret string `json:"console_secret,omitempty"`

	// Dirty is a transient hint to the persistence backend listing
	// which cold collections changed since the last save. Never
	// serialised — mongo backend reads it to skip unchanged
	// collections, JSON backend ignores it (file is whole-state).
	Dirty map[string]bool `json:"-" bson:"-"`
}

// guestEntry is one cached report from a kvm-tools daemon running in a guest.
type guestEntry struct {
	Payload   models.GuestTelemetryPayload `json:"payload"`
	UpdatedAt time.Time                    `json:"updated_at"`
}

// guestToolsFreshness is the window after which a kvm-tools report is considered stale.
const guestToolsFreshness = 5 * time.Minute

// Store is the in-memory data store backed by a JSON file.
type Store struct {
	mu         sync.RWMutex
	hosts      map[string]*models.Host
	vms        map[string][]*models.VM          // keyed by host_id
	storage    map[string][]*models.StoragePool // keyed by host_id
	vmNotes    map[string]string                // keyed by VM UUID (lowercased)
	guestData  map[string]*guestEntry           // keyed by VM UUID (lowercased)
	migrations []*models.MigrationRecord
	users      map[string]*models.User  // keyed by user ID
	groups     map[string]*models.Group // keyed by group ID
	roles      map[string]*models.Role  // keyed by role ID
	sessions   map[string]*models.Session // keyed by session token (memory-only)
	idps       map[string]*models.IdentityProvider // keyed by provider ID
	// kvmToolsUpdateQueue marks VM UUIDs that should be told to self-
	// update on their next telemetry POST. The next /api/guest-telemetry
	// reply for that UUID returns an update_url and the entry clears.
	kvmToolsUpdateQueue map[string]bool
	// consoleSecret is loaded from the JSON store and re-persisted on
	// first access; see GetOrCreateConsoleSecret in console_token.go.
	consoleSecret string
	// audit is the in-memory event ring buffer; capped at
	// auditRetentionMax (see audit.go).
	audit []*models.AuditEvent
	// snapshotPolicies is keyed by policy ID. The scheduler in
	// snapshot_scheduler.go iterates this map once a minute.
	snapshotPolicies map[string]*models.SnapshotPolicy
	// auditShipping is the config for the external log aggregator
	// worker (see audit_shipping.go). Nil means shipping is disabled.
	auditShipping *models.AuditShipping
	// auditShipQueue is the channel the worker reads from; AddAuditEvent
	// non-blockingly sends into it so a slow aggregator can't back up
	// the UI write path.
	auditShipQueue chan *models.AuditEvent
	// notifications (v5.4) — config + dispatch queue. Worker started
	// from main; AddNotification non-blockingly sends into notifyQueue.
	notifications *models.NotificationConfig
	notifyQueue   chan *models.NotificationEvent
	// vmStatePrev tracks the last-seen state of each VM so the
	// notification trigger code can detect transitions
	// (e.g. running→crashed) instead of firing on every telemetry tick.
	vmStatePrev map[string]models.VMState
	// hostOnlinePrev mirrors the same idea for hosts so we only fire
	// host.offline once per outage.
	hostOnlinePrev map[string]bool
	// migrationNotified keys terminal-state notifications by migration
	// ID so we never fan out the same "complete"/"failed" twice when
	// the record is mutated in place by handleCommand → finalize.
	migrationNotified map[string]bool
	// apiTokens (v5.5) keyed by token ID (the public 8-char prefix).
	apiTokens map[string]*models.APIToken
	// hypervisorCerts (v5.10) keyed by host ID. Latest issued leaf per
	// hypervisor wins — rotation overwrites (the old cert becomes a
	// revocation record tracked inside HypervisorCert's RevokedAt).
	hypervisorCerts map[string]*models.HypervisorCert
	// caEnrollTokens (v5.10) keyed by one-time token; in-memory only
	// with short TTL. Consumed by the first successful CSR sign.
	caEnrollTokens map[string]*caEnrollEntry
	features      models.Features
	dataFile      string
	// backend is the pluggable persistence layer (JSON file or Mongo).
	// Wired post-construction by NewStoreWithBackend; NewStore still
	// works for tests and falls back to JSONBackend on dataFile.
	backend Backend

	// hub is the SSE fan-out. Optional — nil is a no-op via SSEHub.Publish,
	// which keeps the store usable in tests and by "go run" without the
	// full HTTP wiring.
	hub *SSEHub
}

// GetFeatures returns a copy of the current global feature flags.
func (s *Store) GetFeatures() models.Features {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.features
}

// SetFeatures replaces the global feature flags and persists them.
func (s *Store) SetFeatures(f models.Features) {
	s.mu.Lock()
	s.features = f
	s.mu.Unlock()
	go s.Save()
	s.hub.Publish(EventSettings, nil)
}

// NewStore builds a Store backed by the JSON file at dataFile. The
// legacy entrypoint — kept for tests and the "no --mongo-url passed"
// branch of main.go. Use NewStoreWithBackend when the operator has
// configured an alternative persistence layer.
func NewStore(dataFile string) *Store {
	return NewStoreWithBackend(NewJSONBackend(dataFile), dataFile)
}

// NewStoreWithBackend builds a Store against an arbitrary Backend.
// dataFile is retained purely for UI display and the "migrate from
// JSON to Mongo" first-boot path (so a MongoBackend'd store still
// knows where the legacy file lives).
func NewStoreWithBackend(backend Backend, dataFile string) *Store {
	s := &Store{
		hosts:     make(map[string]*models.Host),
		vms:       make(map[string][]*models.VM),
		storage:   make(map[string][]*models.StoragePool),
		vmNotes:   make(map[string]string),
		guestData: make(map[string]*guestEntry),
		users:     make(map[string]*models.User),
		groups:    make(map[string]*models.Group),
		roles:     make(map[string]*models.Role),
		sessions:  make(map[string]*models.Session),
		idps:                make(map[string]*models.IdentityProvider),
		kvmToolsUpdateQueue: make(map[string]bool),
		snapshotPolicies:    make(map[string]*models.SnapshotPolicy),
		vmStatePrev:         make(map[string]models.VMState),
		hostOnlinePrev:      make(map[string]bool),
		apiTokens:           make(map[string]*models.APIToken),
		hypervisorCerts:     make(map[string]*models.HypervisorCert),
		caEnrollTokens:      make(map[string]*caEnrollEntry),
		dataFile:            dataFile,
		backend:             backend,
	}
	s.load()
	s.ensureBuiltInRoles()
	return s
}

// ensureBuiltInRoles creates the Administrator / Operator / Viewer roles if
// they don't exist. Safe to call on every start — idempotent.
func (s *Store) ensureBuiltInRoles() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	builtIns := []struct {
		id, name, desc string
		perms          []string
	}{
		{"role_admin", "Administrator", "Full access to all resources and admin functions.",
			[]string{models.PermAdmin}},
		{"role_operator", "Operator", "Manage VM lifecycle, snapshots, and console access.",
			[]string{models.PermVMView, models.PermVMLifecycle, models.PermVMSnapshot,
				models.PermVMConsole, models.PermVMEdit, models.PermHostView,
				models.PermStorageView, models.PermStorageUpload}},
		{"role_viewer", "Viewer", "Read-only access to the inventory.",
			[]string{models.PermVMView, models.PermHostView, models.PermStorageView}},
	}
	for _, r := range builtIns {
		if existing, ok := s.roles[r.id]; ok {
			// Re-sync permissions even if the role exists (built-ins are managed).
			existing.Permissions = r.perms
			existing.BuiltIn = true
			continue
		}
		s.roles[r.id] = &models.Role{
			ID: r.id, Name: r.name, Description: r.desc,
			Permissions: r.perms, BuiltIn: true, CreatedAt: now,
		}
	}
}

// ApplyTelemetry upserts a host and replaces its VMs and storage pools atomically.
func (s *Store) ApplyTelemetry(p *models.TelemetryPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()

	host, ok := s.hosts[p.HostID]
	if !ok {
		host = &models.Host{ID: p.HostID}
		s.hosts[p.HostID] = host
	}

	host.IP = p.IP
	host.AgentPort = p.AgentPort
	host.OS = p.OS
	host.KernelVer = p.KernelVer
	host.LibvirtVer = p.LibvirtVer
	host.CPUUsage = p.CPUUsage
	host.CPUCount = p.CPUCount
	host.MemUsage = p.MemUsage
	host.MemTotalMB = p.MemTotalMB
	host.MemFreeMB = p.MemFreeMB
	host.Uptime = p.Uptime
	host.LastSeen = p.Timestamp
	host.Status = models.HostOnline

	if p.CPUModel != "" {
		host.CPUModel = p.CPUModel
		host.Sockets = p.Sockets
		host.CoresPerSocket = p.CoresPerSocket
		host.ThreadsPerCore = p.ThreadsPerCore
		host.NUMANodes = p.NUMANodes
		host.PCIDevices = p.PCIDevices
		host.GPUs = p.GPUs
	}

	// Stamp host_id onto VMs coming from agent (it may omit it).
	vms := make([]*models.VM, len(p.VMs))
	type stateTransition struct {
		uuid, name, host string
		from, to         models.VMState
	}
	var transitions []stateTransition
	for i := range p.VMs {
		v := p.VMs[i]
		v.HostID = p.HostID
		if v.Networks == nil {
			v.Networks = []models.VMNetwork{}
		}
		s.applyVMSideData(&v)
		vms[i] = &v
		// Track VM state transitions so the dispatcher can fire
		// vm.crashed / vm.recovered notifications. Skip the first
		// observation (prev unset) — otherwise every VM appears to
		// "transition" on server boot.
		key := strings.ToLower(v.UUID)
		if key != "" {
			if prev, ok := s.vmStatePrev[key]; ok && prev != v.State {
				transitions = append(transitions, stateTransition{v.UUID, v.Name, p.HostID, prev, v.State})
			}
			s.vmStatePrev[key] = v.State
		}
	}
	s.vms[p.HostID] = vms
	host.VMCount = len(vms)

	pools := make([]*models.StoragePool, len(p.Storage))
	for i := range p.Storage {
		pool := p.Storage[i]
		pool.HostID = p.HostID
		pools[i] = &pool
	}
	s.storage[p.HostID] = pools
	// Mark this host as online for the offline-recovery edge detector.
	wasOnline, knownHost := s.hostOnlinePrev[p.HostID]
	s.hostOnlinePrev[p.HostID] = true
	// Wake up every live SSE subscriber so the inventory view reflects
	// the agent report immediately instead of on the next client poll.
	s.hub.Publish(EventInventory, map[string]any{"host_id": p.HostID})

	// Notification side effects — fan out outside the lock.
	go func() {
		if knownHost && !wasOnline {
			s.fireNotification(&models.NotificationEvent{
				Type:     NotifyHostRecovered,
				Title:    "Host recovered: " + p.HostID,
				Message:  p.HostID + " is reporting telemetry again.",
				Resource: "host/" + p.HostID,
				Severity: "info",
				At:       time.Now().UTC(),
			})
		}
		for _, t := range transitions {
			switch t.to {
			case models.VMCrashed:
				s.fireNotification(&models.NotificationEvent{
					Type:     NotifyVMCrashed,
					Title:    "VM crashed: " + t.name,
					Message:  t.name + " on " + t.host + " transitioned " + string(t.from) + " → crashed.",
					Resource: "vm/" + t.uuid,
					Severity: "critical",
					At:       time.Now().UTC(),
					Detail:   map[string]string{"host": t.host, "from": string(t.from), "to": string(t.to)},
				})
			case models.VMRunning:
				if t.from == models.VMCrashed || t.from == models.VMUnknown {
					s.fireNotification(&models.NotificationEvent{
						Type:     NotifyVMRecovered,
						Title:    "VM recovered: " + t.name,
						Message:  t.name + " on " + t.host + " is running again (was " + string(t.from) + ").",
						Resource: "vm/" + t.uuid,
						Severity: "info",
						At:       time.Now().UTC(),
					})
				}
			}
		}
	}()
}

// applyVMSideData rehydrates notes and merges guest-tools data onto a VM record.
// Called under the write lock.
func (s *Store) applyVMSideData(v *models.VM) {
	uuid := strings.ToLower(v.UUID)
	if notes, ok := s.vmNotes[uuid]; ok {
		v.Notes = notes
	}
	entry, ok := s.guestData[uuid]
	if !ok {
		return
	}
	if time.Since(entry.UpdatedAt) > guestToolsFreshness {
		return // stale — don't report tools as present
	}
	// Prefer the richer kvm-tools source over qemu-ga when both are present.
	p := entry.Payload
	v.GuestAgent = true
	v.GuestSource = "kvm-tools"
	if p.OS != "" {
		v.GuestOS = p.OS
	}
	if p.Hostname != "" {
		v.GuestHostname = p.Hostname
	}
	if p.Kernel != "" {
		v.GuestKernel = p.Kernel
	}
	if p.Uptime != "" {
		v.GuestUptime = p.Uptime
	}
	if len(p.IPs) > 0 {
		v.GuestIPs = p.IPs
	}
	if len(p.FS) > 0 {
		v.GuestFS = p.FS
	}
	if len(p.Interfaces) > 0 {
		v.GuestInterfaces = p.Interfaces
	}
	if p.KVMToolsVersion != "" {
		v.KVMToolsVersion = p.KVMToolsVersion
	}
	v.KVMToolsUpdateQueued = s.kvmToolsUpdateQueue[strings.ToLower(p.VMUUID)]
}

// SetVMNotes updates the note for the given VM UUID and rewrites any in-memory copies.
func (s *Store) SetVMNotes(uuid, notes string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(uuid)
	if notes == "" {
		delete(s.vmNotes, key)
	} else {
		s.vmNotes[key] = notes
	}
	for _, vms := range s.vms {
		for _, v := range vms {
			if strings.EqualFold(v.UUID, uuid) {
				v.Notes = notes
			}
		}
	}
}

// QueueKVMToolsUpdate marks a set of VM UUIDs to be told to self-update
// on their next telemetry POST. UUIDs are lowercased for the lookup.
func (s *Store) QueueKVMToolsUpdate(uuids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range uuids {
		s.kvmToolsUpdateQueue[strings.ToLower(strings.TrimSpace(u))] = true
	}
	s.hub.Publish(EventInventory, nil)
}

// ConsumeKVMToolsUpdate returns and clears the queued flag for one
// VM UUID — called from handleGuestTelemetry on each report.
func (s *Store) ConsumeKVMToolsUpdate(uuid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(uuid)
	if !s.kvmToolsUpdateQueue[key] {
		return false
	}
	delete(s.kvmToolsUpdateQueue, key)
	return true
}

// QueuedKVMToolsUpdates returns the set of UUIDs currently queued for
// self-update — used by the UI's Update Center to render the
// pending state.
func (s *Store) QueuedKVMToolsUpdates() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.kvmToolsUpdateQueue))
	for k := range s.kvmToolsUpdateQueue {
		out = append(out, k)
	}
	return out
}

// ApplyGuestTelemetry stores a kvm-tools report and re-merges it onto any
// live VM. Returns the canonical libvirt UUID the telemetry was stored
// under — if the guest's reported DMI UUID collided with another VM
// (classic cloned-VM footgun, ark-srv01 vs LLM) the server resolves by
// NIC MAC and returns the corrected UUID so the guest can pin itself
// for subsequent reports.
func (s *Store) ApplyGuestTelemetry(p *models.GuestTelemetryPayload) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Timestamp.IsZero() {
		p.Timestamp = time.Now()
	}
	canonical := s.resolveCanonicalUUID(p)
	originalLower := strings.ToLower(p.VMUUID)
	if !strings.EqualFold(canonical, p.VMUUID) {
		log.Printf("guest-telemetry: UUID collision — payload %s resolved to %s (MAC match)", p.VMUUID, canonical)
		// Evict the stale slot this payload claimed so VMs that share
		// the colliding DMI UUID stop inheriting this guest's data.
		delete(s.guestData, originalLower)
		p.VMUUID = canonical
	}
	key := strings.ToLower(canonical)
	s.guestData[key] = &guestEntry{Payload: *p, UpdatedAt: time.Now()}
	// Also clear any stale fields on the VM whose UUID the payload was
	// previously misattributed to — otherwise it keeps showing the
	// impostor hostname/IPs until the 5-minute freshness window expires.
	for _, vms := range s.vms {
		for _, v := range vms {
			if strings.EqualFold(v.UUID, canonical) {
				s.applyVMSideData(v)
			} else if originalLower != "" && strings.EqualFold(v.UUID, originalLower) && originalLower != key {
				clearStaleGuestData(v)
			}
		}
	}
	return canonical
}

// clearStaleGuestData wipes fields that came from a misattributed
// telemetry post so a cloned-DMI collision doesn't leave the "wrong" VM
// showing ghosted data for 5 minutes after the resolver corrects itself.
func clearStaleGuestData(v *models.VM) {
	v.GuestAgent = false
	v.GuestSource = ""
	v.GuestOS = ""
	v.GuestHostname = ""
	v.GuestKernel = ""
	v.GuestUptime = ""
	v.GuestIPs = nil
	v.GuestFS = nil
	v.GuestInterfaces = nil
	v.KVMToolsVersion = ""
}

// resolveCanonicalUUID picks the libvirt UUID this telemetry payload
// really belongs to. The server's job here is to HELP when the client's
// claim is clearly wrong (cloned DMI on a fresh guest), NOT to overrule
// a correctly-pinned client.
//
// Order of preference:
//   Pass 0: exact payload UUID match against a libvirt VM → trust the
//           client. This respects operators who pinned /var/lib/kvm-tools/
//           uuid via install --uuid and prevents the server from oscillating
//           a cache file when both UUID and MAC were cloned (e.g. ark-srv01
//           whose guest NIC MAC was cloned along with the DMI UUID — MAC
//           match would point to the wrong VM every time).
//   Pass 1: no UUID match — fall back to MAC match. NIC MACs are
//           globally unique on a non-cloned libvirt stack, so this
//           handles "first boot of a freshly cloned guest whose DMI UUID
//           doesn't happen to match any existing VM".
//   Pass 2: neither — store against the client's claim and hope the
//           hvnode agent catches up.
//
// Called under s.mu; caller must hold the write lock.
func (s *Store) resolveCanonicalUUID(p *models.GuestTelemetryPayload) string {
	// Pass 0: the client claims a UUID that exists as a real libvirt
	// domain. Trust it. If two libvirt domains somehow share the same
	// UUID (extremely rare — usually a backup/restore anti-pattern),
	// prefer the running one.
	var uuidHit *models.VM
	for _, vms := range s.vms {
		for _, v := range vms {
			if strings.EqualFold(v.UUID, p.VMUUID) {
				if uuidHit == nil || v.State == "running" {
					uuidHit = v
				}
			}
		}
	}
	if uuidHit != nil {
		return uuidHit.UUID
	}

	// Pass 1: payload UUID is unknown → try NIC MAC match.
	payloadMACs := make(map[string]bool)
	for _, i := range p.Interfaces {
		if i.MAC != "" {
			payloadMACs[strings.ToLower(i.MAC)] = true
		}
	}
	var macHits []*models.VM
	for _, vms := range s.vms {
		for _, v := range vms {
			for _, n := range v.Networks {
				if n.MAC != "" && payloadMACs[strings.ToLower(n.MAC)] {
					macHits = append(macHits, v)
					break
				}
			}
		}
	}
	if len(macHits) == 1 {
		return macHits[0].UUID
	}
	if len(macHits) > 1 {
		// Multiple hits can happen briefly mid-migration. Prefer running.
		for _, v := range macHits {
			if v.State == "running" {
				return v.UUID
			}
		}
		return macHits[0].UUID
	}

	// Pass 2: totally unknown. Store under whatever the payload claimed
	// — fresh VMs may post telemetry before their hvnode agent has
	// reported them.
	return p.VMUUID
}

// AddMigration inserts or updates a migration record.
func (s *Store) AddMigration(r *models.MigrationRecord) {
	s.mu.Lock()
	found := false
	for i, m := range s.migrations {
		if m.ID == r.ID {
			s.migrations[i] = r
			found = true
			break
		}
	}
	if !found {
		s.migrations = append(s.migrations, r)
	}
	// Fire-once tracker for terminal-state notifications. Callers
	// (finalizeMigration / pollMigrationStatus) often mutate the same
	// *r in place before calling us, so a prevStatus comparison off
	// the slice doesn't work — the slice already holds the new value.
	// A per-ID notified set is the simple, correct alternative.
	shouldNotify := false
	if r.Status == "complete" || r.Status == "failed" {
		if s.migrationNotified == nil {
			s.migrationNotified = map[string]bool{}
		}
		if !s.migrationNotified[r.ID] {
			s.migrationNotified[r.ID] = true
			shouldNotify = true
		}
	}
	s.mu.Unlock()
	s.hub.Publish(EventMigration, map[string]any{"id": r.ID, "status": r.Status})

	if shouldNotify {
		switch r.Status {
		case "complete":
			s.fireNotification(&models.NotificationEvent{
				Type:     NotifyMigrationDone,
				Title:    "Migration complete: " + r.VMName,
				Message:  fmt.Sprintf("%s migrated %s → %s in %ds.", r.VMName, r.SourceHost, r.DestHost, r.ElapsedSec),
				Resource: "migration/" + r.ID,
				Severity: "info",
			})
		case "failed":
			s.fireNotification(&models.NotificationEvent{
				Type:     NotifyMigrationFailed,
				Title:    "Migration failed: " + r.VMName,
				Message:  fmt.Sprintf("%s migration %s → %s failed: %s", r.VMName, r.SourceHost, r.DestHost, r.Error),
				Resource: "migration/" + r.ID,
				Severity: "critical",
				Detail:   map[string]string{"error": r.Error, "phase": r.Phase},
			})
		}
	}
}

// GetMigrations returns all migration records, newest first.
func (s *Store) GetMigrations() []*models.MigrationRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.MigrationRecord, len(s.migrations))
	copy(out, s.migrations)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// GetHosts returns a snapshot of all hosts, with offline detection applied.
func (s *Store) GetHosts() []*models.Host {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*models.Host, 0, len(s.hosts))
	for _, h := range s.hosts {
		cp := *h
		if time.Since(h.LastSeen) > 2*time.Minute {
			cp.Status = models.HostOffline
		}
		cp.MTLSReady = s.hypervisorHasActiveCert(h.ID)
		out = append(out, &cp)
	}
	return out
}

// hypervisorHasActiveCert — caller holds s.mu (read lock ok).
//
// Caveat: time.Time zero round-tripped through Mongo comes back as
// time.Unix(-62135596800,0) which has non-zero wall/ext, so IsZero()
// reports false for an un-revoked cert loaded from the DB. Check the
// year instead — anything year<1000 means "no revocation recorded".
func (s *Store) hypervisorHasActiveCert(hostID string) bool {
	c, ok := s.hypervisorCerts[hostID]
	if !ok {
		return false
	}
	if c.RevokedAt.Year() > 1 {
		return false
	}
	return time.Now().Before(c.NotAfter)
}

// HostMTLSReady is the exported variant used by migration routing.
func (s *Store) HostMTLSReady(hostID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hypervisorHasActiveCert(hostID)
}

// GetHost returns a single host by ID, or nil if not found.
func (s *Store) GetHost(id string) *models.Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[id]
	if !ok {
		return nil
	}
	cp := *h
	if time.Since(h.LastSeen) > 2*time.Minute {
		cp.Status = models.HostOffline
	}
	cp.MTLSReady = s.hypervisorHasActiveCert(h.ID)
	return &cp
}

// GetVMs returns all VMs. Pass hostID="" for all hosts.
func (s *Store) GetVMs(hostID string) []*models.VM {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if hostID != "" {
		return append([]*models.VM(nil), s.vms[hostID]...)
	}
	var out []*models.VM
	for _, vms := range s.vms {
		out = append(out, vms...)
	}
	return out
}

// GetStorage returns all storage pools. Pass hostID="" for all hosts.
func (s *Store) GetStorage(hostID string) []*models.StoragePool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if hostID != "" {
		return append([]*models.StoragePool(nil), s.storage[hostID]...)
	}
	var out []*models.StoragePool
	for _, pools := range s.storage {
		out = append(out, pools...)
	}
	return out
}

// GetHostAgentAddr returns "ip:port" for routing commands. Returns "" if host unknown.
func (s *Store) GetHostAgentAddr(hostID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[hostID]
	if !ok || h.IP == "" {
		return ""
	}
	port := h.AgentPort
	if port == 0 {
		port = 8081
	}
	return h.IP + ":" + itoa(port)
}

// MarkOfflineHosts sets hosts that haven't reported for >2 min to offline in memory.
func (s *Store) MarkOfflineHosts() {
	s.mu.Lock()
	changed := false
	var nowOffline []string
	for id, h := range s.hosts {
		if time.Since(h.LastSeen) > 2*time.Minute && h.Status != models.HostOffline {
			h.Status = models.HostOffline
			changed = true
			if s.hostOnlinePrev[id] {
				nowOffline = append(nowOffline, id)
			}
			s.hostOnlinePrev[id] = false
		}
	}
	s.mu.Unlock()
	if changed {
		s.hub.Publish(EventInventory, nil)
	}
	for _, id := range nowOffline {
		s.fireNotification(&models.NotificationEvent{
			Type:     NotifyHostOffline,
			Title:    "Host offline: " + id,
			Message:  id + " has not reported telemetry for >2 minutes.",
			Resource: "host/" + id,
			Severity: "critical",
			At:       time.Now().UTC(),
		})
	}
}

// lastHashes caches FNV hashes of each cold collection from the
// previous Save, letting us compute a dirty set automatically instead
// of instrumenting every mutator. Access is serialised by s.mu.
var lastHashes = struct {
	mu sync.Mutex
	m  map[string]uint64
}{m: map[string]uint64{}}

// computeDirty hashes each cold collection's JSON form, compares to
// the previous snapshot, and returns the set that changed. First call
// returns nil (save everything) so Mongo reaches a known-good state.
func computeDirty(d *persistData) map[string]bool {
	lastHashes.mu.Lock()
	defer lastHashes.mu.Unlock()
	first := len(lastHashes.m) == 0
	out := map[string]bool{}
	fields := map[string]any{
		"users":             d.Users,
		"groups":            d.Groups,
		"roles":             d.Roles,
		"idps":              d.IDPs,
		"sessions":          d.Sessions,
		"features":          d.Features,
		"server_meta":       d.ConsoleSecret,
		"notifications":     d.Notifications,
		"audit_shipping":    d.AuditShipping,
		"api_tokens":        d.APITokens,
		"snapshot_policies": d.SnapshotPolicies,
		"hypervisor_certs":  d.HypervisorCerts,
	}
	for name, v := range fields {
		h := hashJSON(v)
		if first || lastHashes.m[name] != h {
			out[name] = true
		}
		lastHashes.m[name] = h
	}
	if first {
		return nil // nil == write everything on first Save
	}
	return out
}

func hashJSON(v any) uint64 {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

// Save persists the current store state through the configured
// backend (JSON file or Mongo).
func (s *Store) Save() {
	s.mu.RLock()
	d := persistData{
		Hosts:      s.hosts,
		VMs:        s.vms,
		Storage:    s.storage,
		VMNotes:    s.vmNotes,
		GuestData:  s.guestData,
		Migrations: s.migrations,
		Users:      s.users,
		Groups:     s.groups,
		Roles:      s.roles,
		Sessions:   s.sessions,
		IDPs:          s.idps,
		Features:      &s.features,
		Audit:            s.audit,
		AuditShipping:    s.auditShipping,
		SnapshotPolicies: s.snapshotPolicies,
		Notifications:    s.notifications,
		APITokens:        s.apiTokens,
		HypervisorCerts:  s.hypervisorCerts,
		ConsoleSecret:    s.consoleSecret,
	}
	s.mu.RUnlock()
	d.Dirty = computeDirty(&d)

	if s.backend == nil {
		log.Printf("store: no backend configured, skipping save")
		return
	}
	ctx, cancel := contextWithTimeout(15 * time.Second)
	defer cancel()
	if err := s.backend.Save(ctx, &d); err != nil {
		log.Printf("store: backend save error (%s): %v", s.backend.Name(), err)
	}
}

func (s *Store) load() {
	if s.backend == nil {
		return
	}
	ctx, cancel := contextWithTimeout(15 * time.Second)
	defer cancel()
	d, err := s.backend.Load(ctx)
	if err != nil {
		log.Printf("store: backend load error (%s): %v", s.backend.Name(), err)
		return
	}
	if d == nil {
		return // fresh start
	}
	if d.Hosts != nil {
		s.hosts = d.Hosts
	}
	if d.VMs != nil {
		s.vms = d.VMs
	}
	if d.Storage != nil {
		s.storage = d.Storage
	}
	if d.VMNotes != nil {
		s.vmNotes = d.VMNotes
	}
	if d.GuestData != nil {
		s.guestData = d.GuestData
	}
	if d.Migrations != nil {
		s.migrations = d.Migrations
	}
	if d.Users != nil {
		s.users = d.Users
	}
	if d.Groups != nil {
		s.groups = d.Groups
	}
	if d.Roles != nil {
		s.roles = d.Roles
	}
	if d.Sessions != nil {
		// Drop any that expired while we were down.
		now := time.Now()
		for tok, sess := range d.Sessions {
			if now.After(sess.ExpiresAt) {
				delete(d.Sessions, tok)
			}
		}
		s.sessions = d.Sessions
	}
	if d.IDPs != nil {
		s.idps = d.IDPs
	}
	if d.Features != nil {
		s.features = *d.Features
	}
	if d.ConsoleSecret != "" {
		s.consoleSecret = d.ConsoleSecret
	}
	if d.Audit != nil {
		s.audit = d.Audit
	}
	if d.AuditShipping != nil {
		s.auditShipping = d.AuditShipping
	}
	if d.SnapshotPolicies != nil {
		s.snapshotPolicies = d.SnapshotPolicies
	}
	if d.Notifications != nil {
		s.notifications = d.Notifications
	}
	if d.APITokens != nil {
		// Backfill kind on pre-v5.6 tokens. They were all minted from the
		// admin-gated endpoint so "system" preserves existing behavior.
		for _, t := range d.APITokens {
			if t.Kind == "" {
				t.Kind = "system"
			}
		}
		s.apiTokens = d.APITokens
	}
	if d.HypervisorCerts != nil {
		s.hypervisorCerts = d.HypervisorCerts
	}
	log.Printf("store: loaded %d hosts, %d users, %d groups, %d roles from %s",
		len(s.hosts), len(s.users), len(s.groups), len(s.roles), s.dataFile)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 10)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
