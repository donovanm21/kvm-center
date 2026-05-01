package main

// Real-time command progress fan-out (v5.48.1 PR-3a).
//
// Agents POST CommandProgressFrame payloads to
// `/api/agent/command-progress` while a long-running action is in
// flight. We persist the latest frame onto the command_log entry
// (so a fresh SPA page load shows the current phase + percent
// without an SSE warm-up) and broadcast the same payload as an
// SSE `command_progress` event so already-connected clients get
// real-time updates.
//
// Frequency is low: agents are expected to emit a frame every few
// seconds at most. The frame schema mirrors the existing
// MigrationProgress gRPC message so a future converged transport
// is a structural rename.
//
// Auth: agents authenticate via the same `x-kvmc-agent-host`
// header used by the existing telemetry path. mTLS rollout (Tier
// A2 in the v5.47 roadmap proposals) tightens this further.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"kvm-center/internal/models"
)

func handleAgentCommandProgress(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var frame models.CommandProgressFrame
		if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if frame.RequestID == "" {
			writeError(w, http.StatusBadRequest, "request_id required")
			return
		}
		if frame.ProgressPct < 0 {
			frame.ProgressPct = 0
		}
		if frame.ProgressPct > 100 {
			frame.ProgressPct = 100
		}
		if frame.ReportedAt == 0 {
			frame.ReportedAt = time.Now().Unix()
		}
		// If the host_id wasn't carried in the body, fall back to
		// the agent-host header so the SPA can attribute the
		// progress correctly.
		if frame.HostID == "" {
			frame.HostID = strings.TrimSpace(r.Header.Get("x-kvmc-agent-host"))
		}

		// Persist the latest frame on the command_log entry. Drops
		// silently for unknown request_ids (agent ahead of server).
		store.commandLog.RecordProgress(frame.RequestID, &frame)

		// Fan out via SSE for any already-connected SPAs.
		store.hub.Publish(EventCommandProgress, map[string]any{
			"request_id":   frame.RequestID,
			"host_id":      frame.HostID,
			"action":       frame.Action,
			"vm_uuid":      frame.VMUUID,
			"vm_name":      frame.VMName,
			"phase":        frame.Phase,
			"progress_pct": frame.ProgressPct,
			"detail":       frame.Detail,
			"reported_at":  frame.ReportedAt,
		})

		// PR-1c reconciler heartbeat: a fresh progress frame is
		// proof-of-life. Bump LastPolledAt so the reconciler doesn't
		// race the agent for outcome-cache lookups.
		store.commandLog.RecordPoll(frame.RequestID, time.Now())

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}
