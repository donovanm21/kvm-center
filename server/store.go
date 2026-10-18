package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
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
	EntityOverlay  *entityOverlay `json:"entity_bson_overlay,omitempty"`
	restoreOverlay *entityOverlay

	HostHistory      *historyState                       `json:"host_history_state,omitempty"`
	VMTagState       *vmTagState                         `json:"vm_tag_state,omitempty"`
	Hosts            map[string]*models.Host             `json:"hosts"`
	VMs              map[string][]*models.VM             `json:"vms"`
	Storage          map[string][]*models.StoragePool    `json:"storage"`
	VMNotes          map[string]string                   `json:"vm_notes"`
	GuestData        map[string]*guestEntry              `json:"guest_data"`
	Migrations       []*models.MigrationRecord           `json:"migrations,omitempty"`
	Users            map[string]*models.User             `json:"users,omitempty"`
	Groups           map[string]*models.Group            `json:"groups,omitempty"`
	Roles            map[string]*models.Role             `json:"roles,omitempty"`
	Sessions         map[string]*models.Session          `json:"sessions,omitempty"`
	IDPs             map[string]*models.IdentityProvider `json:"idps,omitempty"`
	Features         *models.Features                    `json:"features,omitempty"`
	Audit            []*models.AuditEvent                `json:"audit,omitempty"`
	AuditShipping    *models.AuditShipping               `json:"audit_shipping,omitempty"`
	SnapshotPolicies map[string]*models.SnapshotPolicy   `json:"snapshot_policies,omitempty"`
	Notifications    *models.NotificationConfig          `json:"notifications,omitempty"`
	Metrics          *models.MetricsConfig               `json:"metrics,omitempty"`
	APITokens        map[string]*models.APIToken         `json:"api_tokens,omitempty"`
	HypervisorCerts  map[string]*models.HypervisorCert   `json:"hypervisor_certs,omitempty"`
	CommandLog       map[string]*models.CommandLogEntry  `json:"command_log,omitempty"`
	// ConsoleSecret is the HMAC-SHA256 key the server uses to mint
	// per-session VM console tokens (see console_token.go). Generated
	// on first use; persisted so agents that cached it don't need a
	// re-fetch after a server restart.
	ConsoleSecret string `json:"console_secret,omitempty"`

	// BackupSchedule (v5.43.0) — operator-configured periodic backup
	// destination + retention. Nil when the feature is disabled
	// (default). See backup_scheduler.go.
	BackupSchedule *models.BackupSchedule `json:"backup_schedule,omitempty"`

	// DiscordGateway (v5.52.0) — paired Discord-Gateway sink
	// configuration + state machine. Nil = never configured.
	DiscordGateway *models.DiscordGateway `json:"discord_gateway,omitempty"`

	// RemovedHosts (v5.54.0) — host_ids the operator explicitly
	// retired. Telemetry from a removed host_id is dropped so the
	// agent (if still running) can't silently re-create the row
	// via /api/telemetry. Cleared via /api/hosts/<id>/unretire.
	RemovedHosts map[string]*models.RemovedHost `json:"removed_hosts,omitempty"`

	// PendingEnrollments (v5.55.0) — hypervisor enrolment requests
	// awaiting operator approval. Mirrors the Discord-Gateway
	// pair-request handshake. Sweeper expires entries past
	// ExpiresAt; UI lists pending; approve writes IssuedCertPEM
	// for one-shot delivery on the next agent poll.
	PendingEnrollments map[string]*models.PendingEnrollment `json:"pending_enrollments,omitempty"`

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
	history        *historyState // guarded by mu; never shared across Stores
	historyEnabled bool          // collection only; persisted state is always preserved
	vmTags         map[string][]string
	mu             sync.RWMutex
	// saveWG tracks in-flight async Save goroutines so tests can
	// wait for persistence before tearing down TempDir. Production
	// shutdown uses saves to seal admission and drain without Add/Wait races.
	saveWG sync.WaitGroup
	// saveMu serialises backend.Save calls (v5.60.4). Pre-fix, every
	// AsyncSave spawned its own goroutine and races between them tripped
	// the Mongo backend's drop-and-insert pattern: goroutine A's
	// DeleteMany then InsertMany; goroutine B's DeleteMany after A
	// inserted, then B's InsertMany racing A's writes → E11000 on every
	// save tick at high write rates (telemetry burst, mass migration).
	// Stuck saves left the Discord-Gateway pair handler's state-change
	// stranded in memory because its Save would error out on an
	// earlier collection's E11000 before reaching the discord_gateway
	// collection.
	tagEditMu contextMutex // serializes API tag compare/edit/durable acknowledgement
	saveMu    contextMutex
	work      workSet
	saves     workSet
	shutdown  *shutdownCoordinator
	// Fingerprints of the last successful backend write; guarded by saveMu.
	lastSavedHashes map[string]uint64
	// saveQueued (v5.60.4) coalesces concurrent AsyncSave invocations
	// to at most one in-flight + one pending. AsyncSave atomically
	// CASs false→true to claim the queue slot; the goroutine resets
	// it to false right before running Save so a write that lands
	// during snapshotBytes still schedules a follow-up.
	saveQueued atomic.Bool
	hosts      map[string]*models.Host
	vms        map[string][]*models.VM          // keyed by host_id
	storage    map[string][]*models.StoragePool // keyed by host_id
	vmNotes    map[string]string                // keyed by VM UUID (lowercased)
	guestData  map[string]*guestEntry           // keyed by VM UUID (lowercased)
	migrations []*models.MigrationRecord
	users      map[string]*models.User             // keyed by user ID
	groups     map[string]*models.Group            // keyed by group ID
	roles      map[string]*models.Role             // keyed by role ID
	sessions   map[string]*models.Session          // keyed by session token (memory-only)
	idps       map[string]*models.IdentityProvider // keyed by provider ID
	// kvmToolsUpdateQueue marks VM UUIDs that should be told to self-
	// update on their next telemetry POST. The next /api/guest-telemetry
	// reply for that UUID returns an update_url and the entry clears.
	kvmToolsUpdateQueue map[string]bool
	// consoleSecret is loaded from the JSON store and re-persisted on
	// first access; see GetOrCreateConsoleSecret in console_token.go.
	consoleSecret string
	// backupSchedule (v5.43.0): operator-configured periodic backup.
	// Nil when disabled. See backup_scheduler.go for the worker.
	backupSchedule *models.BackupSchedule
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
	// metrics (v5.13): InfluxDB v2 sink config + sample queue.
	metrics      *models.MetricsConfig
	metricsQueue chan metricSample
	// discordGateway (v5.52) — paired Discord-Gateway sink; see
	// server/discord_gateway.go. Workers (pairing poller, health
	// monitor, send queue) are started by Run().
	discordGateway *models.DiscordGateway
	discordGwQueue chan *models.NotificationEvent
	// removedHosts (v5.54.0) — deny-list keyed by host_id. Telemetry
	// + CSR-sign requests from removed host_ids are rejected so a
	// straggling agent can't silently re-create its row. Cleared via
	// /api/hosts/<id>/unretire.
	removedHosts map[string]*models.RemovedHost
	// pendingEnrollments (v5.55.0) — pre-approval CSR submissions;
	// see server/enrollment_approval.go.
	pendingEnrollments map[string]*models.PendingEnrollment
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
	// hostMetrics (v5.17) is the per-host in-memory ring buffer that
	// backs the in-UI sparklines. Influx remains the canonical long-term
	// store; this is glance-at-the-last-hour material, memory-only, and
	// refills naturally after a restart.
	hostMetrics map[string]*hostMetricRing
	// saveGateOpen controls whether periodic Save is allowed. See
	// isSaveGated for the full reasoning.
	saveGateOpen bool
	// commandLog (v5.32.17) tracks every mutating /api/command invocation
	// for idempotent retries + post-crash reconciliation. Owns its own
	// mutex — never take s.mu and commandLog.mu together.
	commandLog *CommandLog
	features   models.Features
	dataFile   string
	// backend is the pluggable persistence layer (JSON file or Mongo).
	// Wired post-construction by NewStoreWithBackend; NewStore still
	// works for tests and falls back to JSONBackend on dataFile.
	backend       Backend
	entityOverlay *entityOverlay

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
	s.AsyncSave()
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
		hosts:               make(map[string]*models.Host),
		vms:                 make(map[string][]*models.VM),
		storage:             make(map[string][]*models.StoragePool),
		vmNotes:             make(map[string]string),
		guestData:           make(map[string]*guestEntry),
		users:               make(map[string]*models.User),
		groups:              make(map[string]*models.Group),
		roles:               make(map[string]*models.Role),
		sessions:            make(map[string]*models.Session),
		idps:                make(map[string]*models.IdentityProvider),
		kvmToolsUpdateQueue: make(map[string]bool),
		snapshotPolicies:    make(map[string]*models.SnapshotPolicy),
		vmStatePrev:         make(map[string]models.VMState),
		hostOnlinePrev:      make(map[string]bool),
		apiTokens:           make(map[string]*models.APIToken),
		hypervisorCerts:     make(map[string]*models.HypervisorCert),
		caEnrollTokens:      make(map[string]*caEnrollEntry),
		hostMetrics:         make(map[string]*hostMetricRing),
		commandLog:          NewCommandLog(),
		dataFile:            dataFile,
		backend:             backend,
	}
	s.load()
	s.ensureBuiltInRoles()
	s.ensureSecurityDefaults()
	return s
}

// ensureSecurityDefaults flips defaults that v5.47+ wants on by
// default for fresh installs — but doesn't touch an existing store
// (operator may have explicitly opted out of mTLS for a partial
// fleet rollout, and an upgrade flip would brick those agents).
//
// "Fresh install" = no users yet. By the time the first user
// exists the operator has had a chance to enrol agents and can
// flip the feature manually if they want it back off.
func (s *Store) ensureSecurityDefaults() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.users) > 0 {
		return // existing install — preserve operator preference
	}
	if !s.features.GRPCRequireClientCert {
		s.features.GRPCRequireClientCert = true
	}
	// v5.48.1 (PR-3d): notify on task failures by default for fresh
	// installs. Existing fleets stay opt-in until the operator
	// flips it from Settings → Features (informational change; can
	// always be turned back off).
	if !s.features.NotifyOnTaskFailure {
		s.features.NotifyOnTaskFailure = true
	}
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
		{"role_operator", "Operator", "Full VM lifecycle + storage management. No platform admin (users, TLS, host maintenance, audit log).",
			[]string{
				models.PermVMView, models.PermVMLifecycle, models.PermVMCreate,
				models.PermVMDelete, models.PermVMMigrate, models.PermVMSnapshot,
				models.PermVMConsole, models.PermVMEdit,
				models.PermHostView,
				models.PermStorageView, models.PermStorageUpload, models.PermStorageDelete,
			}},
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
//
// Lock discipline (v5.36 P0.1): the write lock is held only for the
// mutation phase. Metrics emit + notification fan-out run after an
// explicit Unlock() so they can't stall behind the ingest path, and
// the snapshots they capture are owned by this invocation (copies
// of *Host / []*VM / []*StoragePool) — no shared mutable state
// crosses the goroutine boundary.
func (s *Store) ApplyTelemetry(p *models.TelemetryPayload) {
	s.mu.Lock()
	observedAt := time.Now() // server observation order, independent of agent timestamps

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
	host.AgentVersion = p.AgentVersion
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

	// v5.55.4 — persist the agent's kvm-migrator SSH pubkey so the
	// fleet-wide authorized_keys endpoint can serve it back. Empty
	// payload means "agent hasn't read it yet"; keep the previous
	// value rather than wiping it.
	if p.MigratorPubkey != "" {
		host.MigratorPubkey = p.MigratorPubkey
	}
	// v5.56.1 — same shape for SSH host keys so the fleet-wide
	// known_hosts blob picks up a rotated key on host reinstall.
	if len(p.SSHHostKeys) > 0 {
		host.SSHHostKeys = append(host.SSHHostKeys[:0], p.SSHHostKeys...)
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
		// Defensive clamp: a non-running VM cannot have live resource
		// usage. Pre-v5.32.14 agents had an underflow bug in
		// vmCPUUsage that surfaced phantom CPU % on stopped domains;
		// guard here so fixed servers render correctly even against
		// unupgraded agents.
		if v.State != models.VMRunning {
			v.CPUUsage = 0
			v.MemUsageMB = 0
			for j := range v.Networks {
				v.Networks[j].RxBytes = 0
				v.Networks[j].TxBytes = 0
			}
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

	// Snapshot everything the post-lock fan-out needs before we
	// release. `hostCopy` is a value copy; `vmsCopy` / `poolsCopy`
	// hold pointers owned by this invocation (future ApplyTelemetry
	// calls build new *VM / *StoragePool allocations, so the
	// goroutine's view stays stable).
	var hostCopy models.Host
	var vmsCopy []*models.VM
	if host != nil {
		hostCopy = *host
	}
	vmsCopy = append(vmsCopy, vms...)
	prefix := ""
	if s.metrics != nil {
		prefix = s.metrics.MeasurementPrefix
	}
	poolsCopy := append([]*models.StoragePool(nil), pools...)

	// Release the write lock before spawning side-effect goroutines.
	// Keeps the ingest path's critical section tight and means the
	// AppendHostMetric / fireNotification paths can take the lock
	// themselves without waiting for us to return.
	s.mu.Unlock()

	// Metrics side effects — fan out outside the lock. Tight loop
	// so the per-VM emit stays bounded on a large fleet.
	s.goWork("telemetry-fanout", func() {
		s.EmitHostMetrics(&hostCopy, prefix)
		s.EmitVMMetrics(vmsCopy, prefix)
		// Feed the in-UI sparkline ring and optional bounded history
		// alongside the external metric writes.
		s.appendHostMetricAt(p.HostID, &hostCopy, poolsCopy, observedAt)
	})

	// Notification side effects — fan out outside the lock.
	s.goWork("telemetry-fanout", func() {
		if knownHost && !wasOnline {
			// v5.56.0 — auto-clear any operator-set "expected down"
			// flag now that the host is reporting again. Without this,
			// a forgotten override would linger and silence future
			// genuine offline events for this host.
			s.clearExpectedDownOnRecovery(p.HostID)
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
	})
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
// (classic cloned-VM footgun where two VMs share UUID + MAC) the server resolves by
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
//
//	Pass 0: exact payload UUID match against a libvirt VM → trust the
//	        client. This respects operators who pinned /var/lib/kvm-tools/
//	        uuid via install --uuid and prevents the server from oscillating
//	        a cache file when both UUID and MAC were cloned (e.g.
//	        whose guest NIC MAC was cloned along with the DMI UUID — MAC
//	        match would point to the wrong VM every time).
//	Pass 1: no UUID match — fall back to MAC match. NIC MACs are
//	        globally unique on a non-cloned libvirt stack, so this
//	        handles "first boot of a freshly cloned guest whose DMI UUID
//	        doesn't happen to match any existing VM".
//	Pass 2: neither — store against the client's claim and hope the
//	        hvnode agent catches up.
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
