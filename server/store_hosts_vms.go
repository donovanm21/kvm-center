package main

// Host + VM + storage accessor methods on *Store — split out of
// store.go in v5.38.0 (P2 #2 part 2). Pure move; no behaviour
// change. Methods stay on *Store; this file is purely a logical
// grouping. Includes the phantom-host pruner since it operates on
// the same surface.

import (
	"log"
	"strings"
	"time"

	"kvm-center/internal/models"
)

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

// DeleteHost removes the host record along with every associated VM,
// storage pool, host-metrics ring, and enrolment cert. Physical VMs
// on the hypervisor are not touched — this just drops the server's
// inventory entries so the host can be cleanly re-enrolled (or walked
// away from). Returns true if a record was removed. Uses short+FQDN
// matching because certs are keyed by both.
func (s *Store) DeleteHost(id string) bool {
	s.mu.Lock()
	removed := false
	if _, ok := s.hosts[id]; ok {
		delete(s.hosts, id)
		removed = true
	}
	delete(s.vms, id)
	delete(s.storage, id)
	delete(s.hostMetrics, id)
	// Remove both short + FQDN cert entries — we've historically
	// issued under both shapes depending on how an agent registered.
	short := strings.SplitN(strings.ToLower(id), ".", 2)[0]
	for cid := range s.hypervisorCerts {
		low := strings.ToLower(cid)
		if low == strings.ToLower(id) || low == short || strings.HasPrefix(low, short+".") {
			delete(s.hypervisorCerts, cid)
		}
	}
	s.mu.Unlock()
	if removed {
		s.AsyncSave()
	}
	return removed
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

// ApplyVMFieldUpdates merges agent-supplied post-edit field values
// into the in-memory VM record so the SPA sees them immediately
// after an EDIT_VM / CHANGE_MEDIA command — no waiting for the
// 30 s telemetry tick. Keys recognised: cpu_count, mem_total_mb,
// boot_dev, cpu_mode, video_model, autostart. Unknown keys
// ignored. Match by UUID preferred; falls back to name on the
// same host.
func (s *Store) ApplyVMFieldUpdates(hostID, vmUUID, vmName string, fields map[string]any) {
	if len(fields) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vms, ok := s.vms[hostID]
	if !ok {
		return
	}
	for _, v := range vms {
		if v == nil {
			continue
		}
		match := (vmUUID != "" && strings.EqualFold(v.UUID, vmUUID)) ||
			(vmUUID == "" && v.Name == vmName)
		if !match {
			continue
		}
		for k, raw := range fields {
			switch k {
			case "cpu_count":
				if n, ok := toInt(raw); ok {
					v.CPUCount = n
				}
			case "mem_total_mb":
				if n, ok := toInt64(raw); ok {
					v.MemTotalMB = uint64(n)
				}
			case "boot_dev":
				if s, ok := raw.(string); ok {
					v.BootDev = s
				}
			case "cpu_mode":
				if s, ok := raw.(string); ok {
					v.CPUMode = s
				}
			case "video_model":
				if s, ok := raw.(string); ok {
					v.VideoModel = s
				}
			case "autostart":
				if b, ok := raw.(bool); ok {
					v.Autostart = b
				}
			}
		}
		break
	}
	s.AsyncSave()
	if s.hub != nil {
		s.hub.Publish(EventInventory, map[string]any{"host_id": hostID})
	}
}

// ApplyVMMetricSample merges a live gRPC metric frame into the VM record
// so the SPA's inventory cache reflects CPU/mem within ~3 s instead of
// waiting for the 15 s telemetry tick. Stopped VMs are skipped — a
// metrics frame for a non-running VM is stale by definition and would
// resurrect the phantom-usage bug fixed in v5.27.10.
func (s *Store) ApplyVMMetricSample(hostID, vmUUID string, cpu float64, memMB int64) {
	if vmUUID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vms, ok := s.vms[hostID]
	if !ok {
		return
	}
	for _, v := range vms {
		if v == nil || !strings.EqualFold(v.UUID, vmUUID) {
			continue
		}
		if v.State != models.VMRunning {
			return
		}
		if cpu >= 0 {
			v.CPUUsage = cpu
		}
		if memMB > 0 {
			v.MemUsageMB = uint64(memMB)
		}
		return
	}
}

// toInt / toInt64: JSON numbers decode as float64 when the target
// is `any`. Convert carefully so we don't lose precision or silently
// accept strings.
func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	}
	return 0, false
}

func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case int:
		return int64(t), true
	case int64:
		return t, true
	}
	return 0, false
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

// GetVMByUUID looks up a single VM across every host. Case-insensitive
// UUID compare. Returns nil when the UUID isn't present in any host's
// cached inventory. Used by GET /api/vms/{uuid} — previously callers
// had to pull /api/vms and filter client-side (v5.33.7).
func (s *Store) GetVMByUUID(uuid string) *models.VM {
	if uuid == "" {
		return nil
	}
	want := strings.ToLower(uuid)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, vms := range s.vms {
		for _, v := range vms {
			if v != nil && strings.ToLower(v.UUID) == want {
				cp := *v
				return &cp
			}
		}
	}
	return nil
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

// phantomHostPruneAfterDefault is the fallback retention when
// Features.PhantomHostPruneAfterDays isn't set (operator hasn't
// touched it, or set to 0). 30 days preserves pre-v5.57.0 behaviour.
// Tier S #13, v5.32.18.
const phantomHostPruneAfterDefault = 30 * 24 * time.Hour

// phantomHostPruneAfter resolves the operator-set retention or
// returns the default. Clamped to [1, 365] days so a typo can't
// turn into a pruner that fires every minute or never fires.
func (s *Store) phantomHostPruneAfter() time.Duration {
	d := s.features.PhantomHostPruneAfterDays
	if d <= 0 {
		return phantomHostPruneAfterDefault
	}
	if d > 365 {
		d = 365
	}
	return time.Duration(d) * 24 * time.Hour
}

// MarkOfflineHosts sets hosts that haven't reported for >2 min to offline in memory.
//
// v5.56.0: when ExpectedDown is set, we still flip the Status to
// offline (so the host card reflects reality and placement guards
// keep rejecting the host as a destination), but we DON'T fire the
// host.offline notification — the operator already knows.
func (s *Store) MarkOfflineHosts() {
	s.mu.Lock()
	changed := false
	var nowOffline []string
	for id, h := range s.hosts {
		if time.Since(h.LastSeen) > 2*time.Minute && h.Status != models.HostOffline {
			h.Status = models.HostOffline
			changed = true
			if s.hostOnlinePrev[id] && !h.ExpectedDown {
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

// PrunePhantomHosts drops hosts that have been offline beyond
// phantomHostPruneAfter(). Called from the same periodic goroutine
// that invokes MarkOfflineHosts. Fires a notification per prune so
// operators have a paper trail. Hypervisor certs tied to pruned
// hosts stay — the operator may still want to revoke them through
// the CA UI, and the cert expiry watcher keeps an eye on them.
func (s *Store) PrunePhantomHosts() {
	s.mu.Lock()
	retention := s.phantomHostPruneAfter()
	cutoff := time.Now().Add(-retention)
	var pruned []string
	for id, h := range s.hosts {
		if h.LastSeen.IsZero() {
			continue
		}
		if h.LastSeen.Before(cutoff) {
			delete(s.hosts, id)
			delete(s.vms, id)
			delete(s.storage, id)
			delete(s.hostOnlinePrev, id)
			delete(s.hostMetrics, id)
			pruned = append(pruned, id)
		}
	}
	s.mu.Unlock()
	if len(pruned) == 0 {
		return
	}
	s.hub.Publish(EventInventory, nil)
	for _, id := range pruned {
		log.Printf("phantom-prune: dropped host %s (offline > %s)", id, retention)
		auditSystem(s, "host.pruned", "host/"+id, map[string]string{
			"reason": "offline > retention",
			"cutoff": retention.String(),
		}, true, "")
	}
}
