package main

import (
	"fmt"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// Validate a single consistent telemetry snapshot. This is an admission hint,
// not proof of execution-time state: the agent probes libvirt again. Keep the
// freshness boundary aligned with GetHost/MarkOfflineHosts (two minutes).
func (s *Store) liveMigrationSource(req models.APICommandRequest, now time.Time) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := s.hosts[req.HostID]
	if h == nil || h.Status != models.HostOnline {
		return fmt.Errorf("live migration requires an available source host")
	}
	if h.LastSeen.IsZero() || now.Sub(h.LastSeen) > 2*time.Minute {
		return fmt.Errorf("live migration source inventory is stale; wait for fresh telemetry")
	}
	for _, vm := range s.vms[req.HostID] {
		if vm == nil {
			continue
		}
		// The agent currently prefers name over UUID. When both are supplied they
		// must identify the same record; never validate a different execution target.
		if req.VMUUID != "" && !strings.EqualFold(vm.UUID, req.VMUUID) {
			continue
		}
		if req.VMName != "" && vm.Name != req.VMName {
			continue
		}
		if req.VMUUID == "" && req.VMName == "" {
			break
		}
		if vm.State != models.VMRunning {
			return fmt.Errorf("live migration requires a known running source VM (reported state: %s); use offline migration for a stopped VM", vm.State)
		}
		return nil
	}
	return fmt.Errorf("live migration requires matching current VM inventory on the source host")
}
