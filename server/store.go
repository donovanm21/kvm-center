package main

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"kvm-center/internal/models"
)

// persistData is the on-disk JSON structure.
type persistData struct {
	Hosts   map[string]*models.Host            `json:"hosts"`
	VMs     map[string][]*models.VM            `json:"vms"`
	Storage map[string][]*models.StoragePool   `json:"storage"`
}

// Store is the in-memory data store backed by a JSON file.
type Store struct {
	mu       sync.RWMutex
	hosts    map[string]*models.Host
	vms      map[string][]*models.VM          // keyed by host_id
	storage  map[string][]*models.StoragePool // keyed by host_id
	dataFile string
}

func NewStore(dataFile string) *Store {
	s := &Store{
		hosts:    make(map[string]*models.Host),
		vms:      make(map[string][]*models.VM),
		storage:  make(map[string][]*models.StoragePool),
		dataFile: dataFile,
	}
	s.load()
	return s
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

	// Stamp host_id onto VMs coming from agent (it may omit it).
	vms := make([]*models.VM, len(p.VMs))
	for i := range p.VMs {
		v := p.VMs[i]
		v.HostID = p.HostID
		if v.Networks == nil {
			v.Networks = []models.VMNetwork{}
		}
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
	defer s.mu.Unlock()
	for _, h := range s.hosts {
		if time.Since(h.LastSeen) > 2*time.Minute {
			h.Status = models.HostOffline
		}
	}
}

// Save persists the current store state to the data file.
func (s *Store) Save() {
	s.mu.RLock()
	d := persistData{
		Hosts:   s.hosts,
		VMs:     s.vms,
		Storage: s.storage,
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
	log.Printf("store: loaded %d hosts from %s", len(s.hosts), s.dataFile)
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
