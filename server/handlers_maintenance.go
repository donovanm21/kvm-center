package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// errHostNotFound is returned by store methods that key by host id —
// defined here so the maintenance-handler package uses a single
// sentinel rather than a string compare.
var errHostNotFound = errors.New("host not found")

// Host maintenance mode (v5.2). Operators toggle a host into
// "maintenance" before patching / rebooting / hardware work; the
// server then refuses:
//   - new VM creation targeting the host
//   - migrations whose destination is the host
// Migrations OFF the host remain allowed — draining is the point.
//
// The flag is advisory in the sense that the hvnode agent doesn't
// know about it (it keeps accepting commands the server sends); the
// server is the authoritative placement gate. We hoist the check
// into handleCommand for MIGRATE/OFFLINE_MIGRATE and into
// handleVMCreate for the new-VM path.

// handleMaintenanceEnter — POST /api/hosts/<id>/maintenance/enter
// Body (optional): {"reason":"patch kernel"}
func handleMaintenanceEnter(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		hostID := hostIDFromMaintenancePath(r.URL.Path)
		if hostID == "" {
			writeError(w, http.StatusBadRequest, "host id required")
			return
		}
		var body struct {
			Reason string `json:"reason"`
			Drain  bool   `json:"drain"`
			Mode   string `json:"mode"` // "live" | "offline"
		}
		_ = json.NewDecoder(r.Body).Decode(&body) // optional
		u := userFromRequest(r)
		actorID := ""
		if u != nil {
			actorID = u.ID
		}
		if err := store.SetHostMaintenance(hostID, true, actorID, body.Reason); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		audit(store, r, AuditHostMaintEnter, "host/"+hostID, map[string]string{
			"reason": body.Reason, "drain": fmt.Sprintf("%v", body.Drain), "mode": body.Mode,
		}, true, "")
		// Optional drain: kick off in the background so the HTTP caller
		// gets an immediate ack. Sequencing + per-VM target picking lives
		// in drainHost; the caller can watch progress through the normal
		// Migrations view.
		if body.Drain {
			mode := body.Mode
			if mode != "live" && mode != "offline" {
				mode = "live"
			}
			// Use the server-wide context so a clean shutdown stops the
			// drain mid-VM rather than leaving an orphan migration in flight.
			go drainHost(serverCtx, store, hostID, mode, actorID)
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// drainHost migrates every running VM off `hostID`, one at a time, in
// the chosen mode. Sequential because the existing concurrency gate in
// handleCommand refuses overlapping migrations on the same source —
// and that's the right call for a homelab fleet where bandwidth and
// CPU are precious. Skips VMs whose target can't be picked (no eligible
// host) and keeps going; surfaces every outcome via audit + the
// Migrations view.
func drainHost(ctx context.Context, store *Store, sourceID, mode, actorID string) {
	log.Printf("drain: starting on %s mode=%s", sourceID, mode)
	vms := store.GetVMs(sourceID)
	for _, vm := range vms {
		if string(vm.State) != "running" && mode == "live" {
			continue
		}
		target := pickDrainTarget(store, sourceID, vm)
		if target == nil {
			auditSystem(store, AuditMigrationStart, "vm/"+vm.UUID, map[string]string{
				"reason": "drain", "source": sourceID, "skipped": "no eligible target",
			}, false, "no eligible destination host")
			continue
		}
		if err := drainOneVM(ctx, store, sourceID, vm, target, mode); err != nil {
			log.Printf("drain: %s → %s failed: %v", vm.Name, target.ID, err)
			auditSystem(store, AuditMigrationStart, "vm/"+vm.UUID, map[string]string{
				"reason": "drain", "source": sourceID, "dest": target.ID, "mode": mode,
			}, false, err.Error())
			continue
		}
		auditSystem(store, AuditMigrationStart, "vm/"+vm.UUID, map[string]string{
			"reason": "drain", "source": sourceID, "dest": target.ID, "mode": mode,
		}, true, "")
	}
	log.Printf("drain: %s complete", sourceID)
}

// pickDrainTarget returns the least-loaded online host that's not the
// source and not in maintenance. Load = current memory usage %; ties
// broken by CPU usage. Returns nil when no candidate exists.
func pickDrainTarget(store *Store, sourceID string, _ *models.VM) *models.Host {
	hosts := store.GetHosts()
	var best *models.Host
	for _, h := range hosts {
		if h.ID == sourceID || h.MaintenanceMode {
			continue
		}
		if h.Status != models.HostOnline {
			continue
		}
		if best == nil {
			cp := *h
			best = &cp
			continue
		}
		if h.MemUsage < best.MemUsage || (h.MemUsage == best.MemUsage && h.CPUUsage < best.CPUUsage) {
			cp := *h
			best = &cp
		}
	}
	return best
}

// drainOneVM kicks the agent's MIGRATE/OFFLINE_MIGRATE for a single
// VM and waits for the terminal state. Mirrors the bookkeeping
// handleCommand does so the migration appears in the Migrations view
// and SSE clients see live progress.
func drainOneVM(ctx context.Context, store *Store, sourceID string, vm *models.VM, dest *models.Host, mode string) error {
	agentAddr := store.GetHostAgentAddr(sourceID)
	if agentAddr == "" {
		return errors.New("source host has no agent address")
	}
	action := "MIGRATE"
	if mode == "offline" {
		action = "OFFLINE_MIGRATE"
	}
	rec := &models.MigrationRecord{
		ID:         fmt.Sprintf("drain-%s-%d", vm.Name, time.Now().UnixNano()),
		VMName:     vm.Name,
		SourceHost: sourceID,
		DestHost:   dest.ID,
		Mode:       mode,
		Status:     "running",
		StartedAt:  time.Now().Unix(),
		Phase:      "starting",
	}
	store.AddMigration(rec)

	body, _ := json.Marshal(models.CommandRequest{
		Action: action,
		VMName: vm.Name,
		VMUUID: vm.UUID,
		Params: map[string]string{"destination": dest.IP},
	})
	// Bound the migration call with a context-based deadline so a
	// caller-cancellation (e.g. server shutdown) propagates promptly
	// instead of waiting for the full 30 min Client.Timeout floor.
	reqCtx, reqCancel := context.WithTimeout(ctx, 30*time.Minute)
	defer reqCancel()
	client := &http.Client{}
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodPost, "http://"+agentAddr+"/command", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		finalizeMigration(store, rec, "failed", "agent unreachable: "+err.Error())
		return err
	}
	defer resp.Body.Close()

	if action == "OFFLINE_MIGRATE" {
		// Async on the agent. Block here until the poller marks it
		// terminal so the next VM in the drain queue doesn't trip the
		// concurrent-migration gate.
		pollMigrationStatus(store, rec, agentAddr)
		if rec.Status != "complete" {
			return errors.New(rec.Error)
		}
		return nil
	}
	// Live migrations are synchronous in the agent; status is the
	// HTTP outcome.
	if resp.StatusCode >= 400 {
		finalizeMigration(store, rec, "failed", fmt.Sprintf("agent replied %d", resp.StatusCode))
		return fmt.Errorf("agent replied %d", resp.StatusCode)
	}
	finalizeMigration(store, rec, "complete", "live drain migration finished")
	return nil
}

// handleMaintenanceExit — POST /api/hosts/<id>/maintenance/exit
func handleMaintenanceExit(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		hostID := hostIDFromMaintenancePath(r.URL.Path)
		if hostID == "" {
			writeError(w, http.StatusBadRequest, "host id required")
			return
		}
		if err := store.SetHostMaintenance(hostID, false, "", ""); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		audit(store, r, AuditHostMaintExit, "host/"+hostID, nil, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// hostIDFromMaintenancePath pulls "X" out of /api/hosts/X/maintenance/(enter|exit).
func hostIDFromMaintenancePath(p string) string {
	const prefix = "/api/hosts/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}
	rest := p[len(prefix):]
	// rest = "<id>/maintenance/(enter|exit)"
	i := strings.Index(rest, "/maintenance/")
	if i <= 0 {
		return ""
	}
	return rest[:i]
}

// SetHostMaintenance flips the flag, captures provenance, and
// publishes an inventory event so the UI refreshes host cards.
func (s *Store) SetHostMaintenance(hostID string, on bool, actor, reason string) error {
	s.mu.Lock()
	h, ok := s.hosts[hostID]
	if !ok {
		s.mu.Unlock()
		return errHostNotFound
	}
	h.MaintenanceMode = on
	if on {
		h.MaintenanceEnteredAt = time.Now().UTC()
		h.MaintenanceBy = actor
		h.MaintenanceReason = reason
	} else {
		h.MaintenanceEnteredAt = time.Time{}
		h.MaintenanceBy = ""
		h.MaintenanceReason = ""
	}
	s.mu.Unlock()
	s.hub.Publish(EventInventory, map[string]any{"host_id": hostID})
	s.AsyncSave()
	return nil
}

// IsHostInMaintenance is the fast gate used by placement-check code.
// Under RLock so command handlers can call it on every mutation without
// contending.
func (s *Store) IsHostInMaintenance(hostID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[hostID]
	return ok && h.MaintenanceMode
}
