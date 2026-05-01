package main

// Agent-side helper for streaming command progress to the server
// (v5.48.1 PR-3a).
//
// Action bodies call ReportCommandProgress(req, pct, phase, detail)
// while they're running; the helper POSTs a CommandProgressFrame
// to the server's /api/agent/command-progress endpoint. The server
// persists the latest frame on its command_log entry and fans the
// payload out via SSE so the SPA's Recent Tasks pane (PR-3b)
// updates in real time.
//
// Best-effort: failures to POST are logged but never fail the
// underlying action. Progress is presentational; the operator
// outcome is still ground truth from ops.db (PR-2a).

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"kvm-center/internal/models"
)

// progressClient is the package-global HTTP client used for
// progress POSTs. Short timeout — these are fire-and-forget.
var progressClient = &http.Client{Timeout: 3 * time.Second}

// progressDropCount tracks consecutive failures so we don't spam
// logs when the server is briefly unreachable.
var progressDropCount atomic.Int32

// ReportCommandProgress sends a single progress frame for the
// given request. Safe to call even when the request_id is empty
// (no-op) so action bodies can use a single helper without
// branching on caller context.
func ReportCommandProgress(req models.CommandRequest, pct int32, phase, detail string) {
	if req.RequestID == "" || telemetryServerURL == "" {
		return
	}
	frame := models.CommandProgressFrame{
		RequestID:   req.RequestID,
		Action:      req.Action,
		VMUUID:      req.VMUUID,
		VMName:      req.VMName,
		Phase:       phase,
		ProgressPct: pct,
		Detail:      detail,
		ReportedAt:  time.Now().Unix(),
	}
	body, err := json.Marshal(frame)
	if err != nil {
		return
	}
	go postProgressFrame(body)
}

func postProgressFrame(body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := telemetryServerURL + "/api/agent/command-progress"
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := progressClient.Do(r)
	if err != nil {
		// Throttle the log — a brief server outage shouldn't fill
		// /var/log with retry noise. Print every 10th drop.
		n := progressDropCount.Add(1)
		if n%10 == 1 {
			log.Printf("agent: progress POST failed (count=%d): %v", n, err)
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		n := progressDropCount.Add(1)
		if n%10 == 1 {
			log.Printf("agent: progress POST returned %d (count=%d)", resp.StatusCode, n)
		}
		return
	}
	progressDropCount.Store(0)
}
