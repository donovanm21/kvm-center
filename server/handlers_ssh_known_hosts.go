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
	"sort"
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
		sshDetail := map[string]string{
			"action": "update",
			"bytes":  itoa(len(s)),
			"actor":  actor,
		}
		audit(store, r, AuditAuditShipping, "ssh_known_hosts", sshDetail, true, "")
		recordOperatorTask(store, r, AuditAuditShipping, "ssh_known_hosts", sshDetail, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"content": s})
	}
}

// handleAgentSSHKnownHosts returns operator-curated content +
// fleet-auto entries as plaintext. SSH host keys are public material
// by design, so the unauthenticated read is safe.
//
// v5.56.1 added the fleet-auto section. Each currently-active host
// (not in removedHosts) contributes one line per reported
// SSHHostKeys entry, in the standard known_hosts format
// `<host>,<ip> <key>`. Agents reconcile this whole blob into
// `/var/lib/kvm-migrator/.ssh/known_hosts` on the hourly tick — so
// a reinstalled host's rotated SSH host key replaces the stale
// entry across the fleet without operator intervention. This is the
// companion to v5.55.4's MigratorPubkey mesh: that one handles
// who-trusts-whom (authorized_keys), this one handles
// which-host-key-is-mine (known_hosts).
func handleAgentSSHKnownHosts(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		f := store.GetFeatures()
		var sb strings.Builder
		// Operator-curated section first so an operator addition
		// always wins on the agent's last-write-wins parse.
		if f.SSHKnownHosts != "" {
			sb.WriteString(f.SSHKnownHosts)
			if !strings.HasSuffix(f.SSHKnownHosts, "\n") {
				sb.WriteByte('\n')
			}
		}
		hosts := store.GetHosts()
		if len(hosts) > 0 {
			sb.WriteString("# fleet-auto (v5.56.1) — agent telemetry SSHHostKeys per host\n")
			// Stable sort by host id so the output is diff-friendly.
			sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
			for _, h := range hosts {
				if h == nil || len(h.SSHHostKeys) == 0 {
					continue
				}
				if store.IsHostRemoved(h.ID) {
					continue
				}
				// Standard known_hosts format. Include host id +
				// short form + IP so SSH matches regardless of which
				// name the caller used.
				prefix := h.ID
				short := strings.SplitN(h.ID, ".", 2)[0]
				if short != "" && short != h.ID {
					prefix = h.ID + "," + short
				}
				if h.IP != "" {
					prefix += "," + h.IP
				}
				for _, k := range h.SSHHostKeys {
					k = strings.TrimSpace(k)
					if k == "" {
						continue
					}
					sb.WriteString(prefix)
					sb.WriteByte(' ')
					sb.WriteString(k)
					sb.WriteByte('\n')
				}
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(sb.String()))
	}
}
