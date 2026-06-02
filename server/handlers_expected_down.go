package main

// Operator-acknowledged "this host is down on purpose, expected to
// return" (v5.56.0).
//
// Separate from MaintenanceMode — that's for an in-fleet host being
// drained for patch/reboot prep. ExpectedDown is for the case where
// the host is literally offline (hardware swap, OS reinstall, network
// change) and the operator wants to:
//   - silence the host.offline notification firehose (teammates
//     shouldn't get paged for a planned downtime)
//   - render a yellow "Expected down" badge in the UI with the
//     operator's note + ETA instead of red "Offline"
//   - have the flag auto-clear when telemetry resumes (so a
//     forgotten flag doesn't stay set after recovery)

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// handleHostExpectedDownSet — POST /api/hosts/<id>/expected-down
// Body: {"reason": "OS reinstall", "expected_return_at": "2026-05-12T10:00:00Z"}
// `reason` is required (operator must explain the override); ETA is optional.
func handleHostExpectedDownSet(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		hostID := hostIDFromExpectedDownPath(r.URL.Path)
		if hostID == "" {
			writeError(w, http.StatusBadRequest, "host id required")
			return
		}
		var body struct {
			Reason           string `json:"reason"`
			ExpectedReturnAt string `json:"expected_return_at"` // RFC3339
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body.Reason = strings.TrimSpace(body.Reason)
		if body.Reason == "" {
			writeError(w, http.StatusBadRequest, "reason required")
			return
		}
		var eta time.Time
		if body.ExpectedReturnAt != "" {
			t, err := time.Parse(time.RFC3339, body.ExpectedReturnAt)
			if err != nil {
				writeError(w, http.StatusBadRequest, "expected_return_at must be RFC3339")
				return
			}
			eta = t.UTC()
		}
		actorID := ""
		if u := userFromRequest(r); u != nil {
			actorID = u.ID
		}
		if err := store.SetHostExpectedDown(hostID, body.Reason, actorID, eta); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		detail := map[string]string{"reason": body.Reason}
		if !eta.IsZero() {
			detail["expected_return_at"] = eta.Format(time.RFC3339)
		}
		audit(store, r, AuditHostExpectedDownSet, "host/"+hostID, detail, true, "")
		recordOperatorTask(store, r, AuditHostExpectedDownSet, "host/"+hostID, detail, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// handleHostExpectedDownClear — POST /api/hosts/<id>/expected-down/clear
func handleHostExpectedDownClear(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		hostID := hostIDFromExpectedDownPath(r.URL.Path)
		if hostID == "" {
			writeError(w, http.StatusBadRequest, "host id required")
			return
		}
		if err := store.ClearHostExpectedDown(hostID); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		audit(store, r, AuditHostExpectedDownClear, "host/"+hostID, nil, true, "")
		recordOperatorTask(store, r, AuditHostExpectedDownClear, "host/"+hostID, nil, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// hostIDFromExpectedDownPath pulls "X" out of
// /api/hosts/X/expected-down(/clear)?
func hostIDFromExpectedDownPath(p string) string {
	const prefix = "/api/hosts/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}
	rest := p[len(prefix):]
	i := strings.Index(rest, "/expected-down")
	if i <= 0 {
		return ""
	}
	return rest[:i]
}

// SetHostExpectedDown is the store-level write — flips the flag,
// captures provenance, publishes an inventory event so the SPA
// refreshes.
func (s *Store) SetHostExpectedDown(hostID, reason, actor string, eta time.Time) error {
	s.mu.Lock()
	h, ok := s.hosts[hostID]
	if !ok {
		s.mu.Unlock()
		return errHostNotFound
	}
	h.ExpectedDown = true
	h.ExpectedDownReason = reason
	h.ExpectedDownBy = actor
	h.ExpectedDownSetAt = time.Now().UTC()
	h.ExpectedReturnAt = eta
	s.mu.Unlock()
	s.AsyncSave()
	s.hub.Publish(EventInventory, map[string]any{"host_id": hostID})
	return nil
}

// ClearHostExpectedDown wipes the flag set. Idempotent for a host
// where the flag is already off (so an operator double-clicking
// "Clear" doesn't 4xx).
func (s *Store) ClearHostExpectedDown(hostID string) error {
	s.mu.Lock()
	h, ok := s.hosts[hostID]
	if !ok {
		s.mu.Unlock()
		return errHostNotFound
	}
	h.ExpectedDown = false
	h.ExpectedDownReason = ""
	h.ExpectedDownBy = ""
	h.ExpectedDownSetAt = time.Time{}
	h.ExpectedReturnAt = time.Time{}
	s.mu.Unlock()
	s.AsyncSave()
	s.hub.Publish(EventInventory, map[string]any{"host_id": hostID})
	return nil
}

// clearExpectedDownOnRecovery is called from the telemetry path when
// a host that was offline comes back. Auto-clears the operator-set
// flag so a forgotten downtime override doesn't linger. Returns true
// if the flag was actually set (caller can audit the auto-clear).
func (s *Store) clearExpectedDownOnRecovery(hostID string) bool {
	s.mu.Lock()
	h, ok := s.hosts[hostID]
	if !ok || !h.ExpectedDown {
		s.mu.Unlock()
		return false
	}
	h.ExpectedDown = false
	h.ExpectedDownReason = ""
	h.ExpectedDownBy = ""
	h.ExpectedDownSetAt = time.Time{}
	h.ExpectedReturnAt = time.Time{}
	s.mu.Unlock()
	return true
}

// strandedVMCount returns how many VMs were last seen on `hostID`.
// Used by the UI on offline hosts to surface "N VMs stranded" so the
// operator knows the blast radius of the downtime.
func (s *Store) strandedVMCount(hostID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vms, ok := s.vms[hostID]
	if !ok {
		return 0
	}
	return len(vms)
}

// Shut the unused-import linter up when models isn't otherwise needed
// by the file.
var _ = models.HostOffline
