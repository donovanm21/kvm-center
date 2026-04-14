package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
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
		audit(store, r, AuditHostMaintEnter, "host/"+hostID, map[string]string{"reason": body.Reason}, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
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
	go s.Save()
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
