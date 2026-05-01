package main

// Operator-driven command cancellation (v5.48 PR-2f).
//
// `POST /api/command/cancel?request_id=<id>` looks up the command-
// log entry, finds the host that owns it, and proxies to the
// agent's `/command/cancel`. Updates the local command_log so the
// SPA reflects the cancellation immediately even if the agent's
// reconciliation tick hasn't landed yet.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func handleCommandCancel(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		id := r.URL.Query().Get("request_id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "missing request_id")
			return
		}
		var body struct {
			Reason string `json:"reason"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		reason := body.Reason
		if reason == "" {
			reason = "cancelled by operator"
		}

		// Find the entry to learn which host owns it.
		var target *modelsCommandLogShim
		for _, e := range store.commandLog.List(0) {
			if e.RequestID == id {
				target = &modelsCommandLogShim{Action: e.Action, HostID: e.HostID, VMUUID: e.VMUUID, VMName: e.VMName, Outcome: e.Outcome}
				break
			}
		}
		if target == nil {
			writeError(w, http.StatusNotFound, "request_id not found in command log")
			return
		}
		if target.Outcome != "pending" {
			writeJSON(w, http.StatusOK, map[string]any{
				"request_id": id,
				"state":      target.Outcome,
				"note":       "already terminal; idempotent no-op",
			})
			return
		}

		addr := store.GetHostAgentAddr(target.HostID)
		if addr == "" {
			writeError(w, http.StatusServiceUnavailable, "host has no agent address")
			return
		}
		buf, _ := json.Marshal(map[string]string{"reason": reason})
		url := fmt.Sprintf("http://%s/command/cancel?request_id=%s", addr, id)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewReader(buf))
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent cancel failed: "+err.Error())
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			writeError(w, resp.StatusCode, "agent rejected cancel: "+string(respBody))
			return
		}

		// Mirror the agent's verdict into our command_log so the
		// SPA reflects it immediately without waiting on the next
		// reconciler tick. We only mark cancelled on the happy path.
		store.commandLog.Update(id, "cancelled", reason, http.StatusOK, false)
		store.hub.Publish(EventCommand, map[string]any{
			"request_id": id,
			"outcome":    "cancelled",
			"host_id":    target.HostID,
			"action":     target.Action,
		})
		auditSystem(store, "command.cancelled", "vm/"+target.VMUUID, map[string]string{
			"request_id": id,
			"action":     target.Action,
			"host_id":    target.HostID,
			"vm_name":    target.VMName,
			"reason":     reason,
		}, true, "")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBody)
	}
}

// modelsCommandLogShim is a minimal copy of the fields handler
// needs from a CommandLogEntry — kept local so the import surface
// of this file stays tiny.
type modelsCommandLogShim struct {
	Action  string
	HostID  string
	VMUUID  string
	VMName  string
	Outcome string
}
