package main

// Storage-pool lifecycle proxy (v5.58.0).
//
// Pre-v5.58.0 kvmc could only READ pools (via /vm-create-info). To
// add a pool the operator had to ssh into the hypervisor and run
// `virsh pool-define-as` by hand — surfaced by the hostD dogfood
// where a freshly enrolled host had zero pools and the create-VM
// wizard's pool dropdown was empty.
//
// This handler proxies each call to the host's agent. Route shape:
//
//   POST   /api/hosts/<id>/storage/pools                              → /pool-define
//   POST   /api/hosts/<id>/storage/pools/<name>/{start,stop,
//                                   autostart,no-autostart}           → /pool/<name>/<action>
//   DELETE /api/hosts/<id>/storage/pools/<name>                       → DELETE /pool/<name>
//
// Permission gate: storage.upload (operator role bundle). No new
// permission constant — pool management lives in the same family
// as upload + delete from a least-privilege standpoint: someone
// who can write to a pool can already define one in libvirt.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// hostFromStoragePoolPath pulls "X" out of
// /api/hosts/X/storage/pools[/<rest>]. Returns "" if the path
// shape doesn't match.
func hostFromStoragePoolPath(p string) (host, rest string) {
	const prefix = "/api/hosts/"
	if !strings.HasPrefix(p, prefix) {
		return "", ""
	}
	q := p[len(prefix):]
	const mid = "/storage/pools"
	i := strings.Index(q, mid)
	if i <= 0 {
		return "", ""
	}
	host = q[:i]
	rest = strings.TrimPrefix(q[i+len(mid):], "/")
	return host, rest
}

// handleStoragePools is the single entry point the mux dispatches
// every /api/hosts/<id>/storage/pools[/<rest>] call to. We branch
// on (method, rest) to pick the right agent path.
func handleStoragePools(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostID, rest := hostFromStoragePoolPath(r.URL.Path)
		if hostID == "" {
			writeError(w, http.StatusBadRequest, "host id required")
			return
		}
		agentAddr := store.GetHostAgentAddr(hostID)
		if agentAddr == "" {
			writeError(w, http.StatusNotFound, "host not found or no agent address")
			return
		}
		actor := ""
		if u := userFromRequest(r); u != nil {
			actor = u.ID
		}

		var (
			agentPath string
			method    string
			body      []byte
		)
		auditDetail := map[string]string{}
		switch {
		case r.Method == http.MethodPost && rest == "":
			// Create a new pool.
			b, err := io.ReadAll(r.Body)
			if err != nil {
				writeError(w, http.StatusBadRequest, "read: "+err.Error())
				return
			}
			// Mirror the body verbatim — agent does its own validation.
			body = b
			// Pull name + path for the audit trail without re-decoding
			// on the agent side.
			var probe struct {
				Name      string `json:"name"`
				Path      string `json:"path"`
				Autostart bool   `json:"autostart"`
			}
			_ = json.Unmarshal(b, &probe)
			auditDetail["pool"] = probe.Name
			auditDetail["path"] = probe.Path
			if probe.Autostart {
				auditDetail["autostart"] = "true"
			}
			method = http.MethodPost
			agentPath = "/pool-define"
		case r.Method == http.MethodPost && strings.Contains(rest, "/"):
			// /pool/<name>/<action> — start, stop, autostart, no-autostart.
			parts := strings.Split(rest, "/")
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				writeError(w, http.StatusBadRequest, "expected /<name>/<action>")
				return
			}
			method = http.MethodPost
			agentPath = "/pool/" + parts[0] + "/" + parts[1]
			auditDetail["pool"] = parts[0]
			auditDetail["action"] = parts[1]
		case r.Method == http.MethodDelete && rest != "" && !strings.Contains(rest, "/"):
			method = http.MethodDelete
			agentPath = "/pool/" + rest
			auditDetail["pool"] = rest
			auditDetail["action"] = "delete"
		default:
			writeError(w, http.StatusBadRequest, "unsupported storage-pools route")
			return
		}
		auditDetail["host_id"] = hostID
		auditDetail["actor"] = actor

		url := fmt.Sprintf("http://%s%s", agentAddr, agentPath)
		req, err := http.NewRequest(method, url, bytes.NewReader(body))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "build agent request: "+err.Error())
			return
		}
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		client := store.newAgentClient(hostID, 30*time.Second)
		resp, err := client.Do(req)
		if err != nil {
			audit(store, r, AuditStoragePool, "host/"+hostID, auditDetail, false, err.Error())
			recordOperatorTask(store, r, AuditStoragePool, "host/"+hostID, auditDetail, false, err.Error())
			writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		ok := resp.StatusCode < 400
		errMsg := ""
		if !ok {
			errMsg = strings.TrimSpace(string(respBody))
		}
		audit(store, r, AuditStoragePool, "host/"+hostID, auditDetail, ok, errMsg)
		recordOperatorTask(store, r, AuditStoragePool, "host/"+hostID, auditDetail, ok, errMsg)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
	}
}
