package main

// Tests for the v5.48.1 PR-3a progress endpoint.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func TestProgress_AcceptsFrameAndPersists(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	s.hub = NewSSEHub()
	s.commandLog.Record(&models.CommandLogEntry{
		RequestID:   "req_p",
		Action:      "DELETE_SNAPSHOT",
		HostID:      "hv1",
		Outcome:     "pending",
		SubmittedAt: time.Now(),
	})

	frame := models.CommandProgressFrame{
		RequestID:   "req_p",
		Phase:       "commit",
		ProgressPct: 42,
		Detail:      "qemu-img commit running",
	}
	body, _ := json.Marshal(frame)
	r := httptest.NewRequest(http.MethodPost, "/api/agent/command-progress", bytes.NewReader(body))
	r.Header.Set("x-kvmc-agent-host", "hv1")
	rr := httptest.NewRecorder()

	handleAgentCommandProgress(s)(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	got := s.commandLog.List(0)[0]
	if got.LastProgress == nil {
		t.Fatal("LastProgress not persisted")
	}
	if got.LastProgress.ProgressPct != 42 {
		t.Fatalf("progress = %d, want 42", got.LastProgress.ProgressPct)
	}
	if got.LastProgress.Phase != "commit" {
		t.Fatalf("phase = %q", got.LastProgress.Phase)
	}
	if got.LastProgress.HostID != "hv1" {
		t.Fatalf("host_id should fall back to header, got %q", got.LastProgress.HostID)
	}
}

func TestProgress_PercentClamped(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	s.hub = NewSSEHub()
	s.commandLog.Record(&models.CommandLogEntry{RequestID: "req_clamp", Outcome: "pending", SubmittedAt: time.Now()})

	for _, in := range []int32{-10, 999} {
		frame := models.CommandProgressFrame{RequestID: "req_clamp", ProgressPct: in}
		body, _ := json.Marshal(frame)
		r := httptest.NewRequest(http.MethodPost, "/api/agent/command-progress", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		handleAgentCommandProgress(s)(rr, r)
		got := s.commandLog.List(0)[0]
		if got.LastProgress.ProgressPct < 0 || got.LastProgress.ProgressPct > 100 {
			t.Fatalf("input=%d clamped to %d (out of range)", in, got.LastProgress.ProgressPct)
		}
	}
}

func TestProgress_MissingRequestID(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	s.hub = NewSSEHub()
	body, _ := json.Marshal(models.CommandProgressFrame{ProgressPct: 1})
	r := httptest.NewRequest(http.MethodPost, "/api/agent/command-progress", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	handleAgentCommandProgress(s)(rr, r)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestProgress_TerminalEntryNotMutated(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	s.hub = NewSSEHub()
	s.commandLog.Record(&models.CommandLogEntry{RequestID: "req_done", Outcome: "pending", SubmittedAt: time.Now()})
	s.commandLog.Update("req_done", "success", "ok", 200, false)

	body, _ := json.Marshal(models.CommandProgressFrame{RequestID: "req_done", ProgressPct: 50})
	r := httptest.NewRequest(http.MethodPost, "/api/agent/command-progress", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	handleAgentCommandProgress(s)(rr, r)

	got := s.commandLog.List(0)[0]
	if got.LastProgress != nil {
		t.Fatalf("terminal row gained a progress frame: %+v", got.LastProgress)
	}
}

func TestProgress_UnknownRequestIDDropsSilently(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	s.hub = NewSSEHub()
	body, _ := json.Marshal(models.CommandProgressFrame{RequestID: "req_unknown", ProgressPct: 1})
	r := httptest.NewRequest(http.MethodPost, "/api/agent/command-progress", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	handleAgentCommandProgress(s)(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("unknown id should still 200 (idempotent), got %d", rr.Code)
	}
}
