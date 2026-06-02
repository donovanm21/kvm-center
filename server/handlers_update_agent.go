package main

// Per-host agent update (v5.27.1). Operator clicks "Update agent" on
// Host detail; server proxies the request to the agent's /self-update
// endpoint. Agent downloads the latest binary from /downloads/ and
// exits for systemd to restart. Gated on host.manage.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func handleUpdateAgent(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		// Path is /api/hosts/{id}/update-agent — strip prefix + suffix.
		path := strings.TrimPrefix(r.URL.Path, "/api/hosts/")
		id := strings.TrimSuffix(path, "/update-agent")
		if id == "" || id == path {
			writeError(w, http.StatusBadRequest, "path must be /api/hosts/<id>/update-agent")
			return
		}
		h := store.GetHost(id)
		if h == nil {
			writeError(w, http.StatusNotFound, "host not found")
			return
		}
		addr := store.GetHostAgentAddr(id)
		if addr == "" {
			writeError(w, http.StatusServiceUnavailable, "no agent address for host")
			return
		}
		// Agent downloads from whichever server URL it knows; we don't
		// need to dictate one here.
		client := store.newAgentClient(id, 60*time.Second)
		resp, err := client.Post("http://"+addr+"/self-update", "application/json", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			// Agent may have already exited before the HTTP response
			// could reach us — treat connection-reset as likely-success
			// and let the operator refresh to verify the new version.
			if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "connection reset") {
				writeJSON(w, http.StatusOK, map[string]any{
					"success": true,
					"message": "agent exited for restart; give it 5 s then reload Host detail to see the new version",
				})
				audit(store, r, AuditHostUpdateAgent, "host/"+id, map[string]string{"result": "exit-before-response"}, true, "")
				return
			}
			writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
			audit(store, r, AuditHostUpdateAgent, "host/"+id, map[string]string{"error": err.Error()}, false, err.Error())
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		success := resp.StatusCode < 400
		audit(store, r, AuditHostUpdateAgent, "host/"+id, map[string]string{
			"status": http.StatusText(resp.StatusCode),
		}, success, string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		_ = json.RawMessage(body)
	}
}
