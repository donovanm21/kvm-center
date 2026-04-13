package main

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/models"
)

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
	features   models.Features
	dataFile   string

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

func NewStore(dataFile string) *Store {
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
		dataFile:            dataFile,
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
	for i := range p.VMs {
		v := p.VMs[i]
		v.HostID = p.HostID
		if v.Networks == nil {
			v.Networks = []models.VMNetwork{}
		}
		s.applyVMSideData(&v)
		vms[i] = &v
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
	// Wake up every live SSE subscriber so the inventory view reflects
	// the agent report immediately instead of on the next client poll.
	s.hub.Publish(EventInventory, map[string]any{"host_id": p.HostID})
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

// ApplyGuestTelemetry stores a kvm-tools report and re-merges it onto any live VM.
func (s *Store) ApplyGuestTelemetry(p *models.GuestTelemetryPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Timestamp.IsZero() {
		p.Timestamp = time.Now()
	}
	key := strings.ToLower(p.VMUUID)
	s.guestData[key] = &guestEntry{Payload: *p, UpdatedAt: time.Now()}
	for _, vms := range s.vms {
		for _, v := range vms {
			if strings.EqualFold(v.UUID, p.VMUUID) {
				s.applyVMSideData(v)
			}
		}
	}
}

// AddMigration inserts or updates a migration record.
func (s *Store) AddMigration(r *models.MigrationRecord) {
	s.mu.Lock()
	for i, m := range s.migrations {
		if m.ID == r.ID {
			s.migrations[i] = r
			s.mu.Unlock()
			s.hub.Publish(EventMigration, map[string]any{"id": r.ID, "status": r.Status})
			return
		}
	}
	s.migrations = append(s.migrations, r)
	s.mu.Unlock()
	s.hub.Publish(EventMigration, map[string]any{"id": r.ID, "status": r.Status})
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
		out = append(out, &cp)
	}
	return out
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
	for _, h := range s.hosts {
		if time.Since(h.LastSeen) > 2*time.Minute && h.Status != models.HostOffline {
			h.Status = models.HostOffline
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.hub.Publish(EventInventory, nil)
	}
}

// Save persists the current store state to the data file.
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
		IDPs:       s.idps,
		Features:   &s.features,
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		log.Printf("store: marshal error: %v", err)
		return
	}
	if err := os.WriteFile(s.dataFile, data, 0644); err != nil {
		log.Printf("store: write error: %v", err)
	}
}

func (s *Store) load() {
	data, err := os.ReadFile(s.dataFile)
	if err != nil {
		return // fresh start
	}
	var d persistData
	if err := json.Unmarshal(data, &d); err != nil {
		log.Printf("store: load error: %v", err)
		return
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
