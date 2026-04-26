package main

// SSH known_hosts admin + agent endpoints (v5.46.0). Centralises
// the operator-managed `known_hosts` file that agents persist to
// `/var/lib/kvm-migrator/.ssh/known_hosts` when
// `Features.SSHStrictHostKeyChecking` is on. Without this the
// strict-host-key flag was effectively unusable on a fleet of
// 30+ hypervisors — the operator would have to edit every agent.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const sshKnownHostsMaxBytes = 256 * 1024 // 256 KB ceiling

func handleSSHKnownHostsGet(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		f := store.GetFeatures()
		writeJSON(w, http.StatusOK, map[string]string{"content": f.SSHKnownHosts})
	}
}

func handleSSHKnownHostsPut(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "PUT/POST only")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, sshKnownHostsMaxBytes)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "read: "+err.Error())
			return
		}
		// PUT can be JSON `{"content":"…"}` (UI) or raw text body
		// (curl). Detect by Content-Type.
		s := string(body)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var req struct {
				Content string `json:"content"`
			}
			if jerr := json.Unmarshal(body, &req); jerr != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON: "+jerr.Error())
				return
			}
			s = req.Content
		}
		// Best-effort sanity: reject embedded NULs / binary blobs.
		if strings.ContainsRune(s, 0) {
			writeError(w, http.StatusBadRequest, "known_hosts must be ASCII text")
			return
		}
		f := store.GetFeatures()
		f.SSHKnownHosts = s
		store.SetFeatures(f)
		actor := ""
		if u := userFromRequest(r); u != nil {
			actor = u.ID
		}
		audit(store, r, AuditAuditShipping, "ssh_known_hosts", map[string]string{
			"action": "update",
			"bytes":  itoa(len(s)),
			"actor":  actor,
		}, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"content": s})
	}
}

// handleAgentSSHKnownHosts returns the operator-curated content as
// plaintext, intended for `curl | tee /var/lib/kvm-migrator/.ssh/known_hosts`
// from the agent's update path. No SPA session required — agents
// on the management VLAN already have HTTPS access. SSH host keys
// are public material by design, so the unauthenticated read is
// safe.
func handleAgentSSHKnownHosts(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		f := store.GetFeatures()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(f.SSHKnownHosts))
	}
}
