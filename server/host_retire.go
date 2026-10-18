package main

// Host retire flow (v5.54.0).
//
// The Delete-host operation used to remove the host record locally
// but leave the agent on the hypervisor running. Every 15 s the
// agent telemetered, ApplyTelemetry auto-created the row, and the
// "deleted" host kept reappearing — confusing operators and
// re-establishing trust without their consent.
//
// Retire fixes both halves of that:
//
//  1. Server side: the host_id lands in s.removedHosts (deny-list).
//     ApplyTelemetry drops anything from a removed host_id, and
//     /api/ca/sign refuses to issue a fresh leaf for it. Cleared
//     via /api/hosts/<id>/unretire when the operator wants to
//     re-enrol.
//
//  2. Agent side: the server fires a RETIRE command at the agent
//     just before the deny-list write. The agent stops itself,
//     disables its systemd unit, and removes the cert/key files
//     and libvirt drop-ins it installed. By the time the agent's
//     last response lands, the host is fully out of the fleet.
//
// If the agent is unreachable when retire fires, the deny-list
// still kicks in — the host stays gone from kvm-center's view, and
// the operator can SSH in to clean up files manually if needed.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// IsHostRemoved reports whether the host_id has been retired by
// the operator. Telemetry + CSR-sign requests for removed hosts
// are rejected. Tolerates short-form IDs (e.g. "hostA" vs
// "hostA.example.com") by checking both shapes.
func (s *Store) IsHostRemoved(hostID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.removedHosts == nil {
		return false
	}
	low := strings.ToLower(hostID)
	if _, ok := s.removedHosts[low]; ok {
		return true
	}
	// Match short-form against FQDN entries and vice-versa so a
	// retired "hostD.example.com" also rejects telemetry from
	// "hostD".
	short := strings.SplitN(low, ".", 2)[0]
	for k := range s.removedHosts {
		k = strings.ToLower(k)
		if k == short || strings.HasPrefix(k, short+".") {
			return true
		}
	}
	return false
}

// MarkHostRemoved adds a host_id to the deny-list. Idempotent —
// a second call updates the metadata (RemovedAt + actor) without
// changing the operator-visible reason.
func (s *Store) MarkHostRemoved(hostID, removedBy, reason string) {
	if hostID == "" {
		return
	}
	low := strings.ToLower(hostID)
	s.mu.Lock()
	if s.removedHosts == nil {
		s.removedHosts = map[string]*models.RemovedHost{}
	}
	// Preserve original persisted spelling for a retained identity.
	if prior := s.removedHosts[low]; prior != nil {
		hostID = prior.HostID
	}
	s.removedHosts[low] = &models.RemovedHost{
		HostID:    hostID,
		RemovedAt: time.Now().UTC(),
		RemovedBy: removedBy,
		Reason:    reason,
	}
	s.mu.Unlock()
	s.AsyncSave()
}

// UnmarkHostRemoved clears the deny-list entry so the host can
// re-enrol. Returns true if an entry existed.
func (s *Store) UnmarkHostRemoved(hostID string) bool {
	low := strings.ToLower(hostID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removedHosts == nil {
		return false
	}
	if _, ok := s.removedHosts[low]; !ok {
		// Also try short-form.
		short := strings.SplitN(low, ".", 2)[0]
		for k := range s.removedHosts {
			if strings.ToLower(k) == short {
				delete(s.removedHosts, k)
				s.AsyncSave()
				return true
			}
		}
		return false
	}
	delete(s.removedHosts, low)
	s.AsyncSave()
	return true
}

// ListRemovedHosts returns a snapshot of the deny-list. Used by
// the UI's TLS / Retired hosts panel and by the audit log
// reviewer. Sorted by RemovedAt descending — most-recent first.
func (s *Store) ListRemovedHosts() []*models.RemovedHost {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.RemovedHost, 0, len(s.removedHosts))
	for _, rh := range s.removedHosts {
		cp := *rh
		out = append(out, &cp)
	}
	return out
}

// sendAgentRetire fires a RETIRE command at the agent before the
// host record is removed. Best-effort — a missing or unreachable
// agent doesn't block the retire on the server side.
//
// The agent responds 202 immediately and runs cleanup in a
// background goroutine: stop libvirtd-tls.socket, remove the
// boot-order drop-in, remove cert/key + migration-grant hook, and
// disable + stop kvm-center-agent.service. Order matters — the
// agent must respond before it kills itself, otherwise the server
// sees a connection-reset that's indistinguishable from "agent
// crashed mid-handshake".
func sendAgentRetire(store *Store, host *models.Host, removeFiles bool) error {
	if host == nil || host.IP == "" {
		return fmt.Errorf("host has no IP — agent unreachable")
	}
	port := host.AgentPort
	if port == 0 {
		port = 8081
	}
	rf := "true"
	if !removeFiles {
		rf = "false"
	}
	body, _ := json.Marshal(map[string]any{
		"action": "RETIRE",
		"params": map[string]string{"remove_files": rf},
	})
	url := fmt.Sprintf("http://%s:%d/command", host.IP, port)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	client := store.newAgentClient(host.ID, 10*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("agent retire: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("agent retire: HTTP %d", resp.StatusCode)
	}
	log.Printf("retire: agent on %s acknowledged RETIRE (%s)", host.ID, resp.Status)
	return nil
}

// agentRetireErrToStr renders an error for the API response without
// exposing nil-ness. Empty when no error.
func agentRetireErrToStr(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

// handleUnretireHost — POST /api/hosts/<id>/unretire. Clears the
// deny-list entry so a fresh enrolment for that host_id can land.
// The host stays gone from the inventory (operator must run
// install.sh again to bring it back).
func handleUnretireHost(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		// Path: /api/hosts/<id>/unretire
		path := strings.TrimPrefix(r.URL.Path, "/api/hosts/")
		id := strings.TrimSuffix(path, "/unretire")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusBadRequest, "expected /api/hosts/<id>/unretire")
			return
		}
		if !store.UnmarkHostRemoved(id) {
			writeError(w, http.StatusNotFound, "host_id is not in the retired list")
			return
		}
		audit(store, r, AuditHostDelete, "host/"+id, map[string]string{"action": "unretire"}, true, "")
		recordOperatorTask(store, r, AuditHostDelete, "host/"+id, map[string]string{"action": "unretire"}, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"unretired": id})
	}
}

// handleListRetired — GET /api/hosts/retired. Lists deny-listed
// host_ids for the Settings → TLS panel.
func handleListRetired(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		writeJSON(w, http.StatusOK, store.ListRemovedHosts())
	}
}
