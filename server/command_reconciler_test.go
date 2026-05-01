package main

// Tests for the v5.47.2 live command reconciler. The reconciler
// resolves command-log entries left "pending" by handleCommand when
// its initial POST /command call timed out / errored / 5xx'd.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// fakeAgent is the minimum stub the reconciler needs: a
// /command-outcome handler whose response is configurable per-test.
type fakeAgent struct {
	t      *testing.T
	server *httptest.Server
	calls  atomic.Int32
	// hits maps request_id → 200 outcome to return; otherwise 404.
	hits map[string]models.CommandOutcomeResponse
	// failNext, when true, makes the first incoming request fail
	// with a transport-style error before we set it back to false.
	failNext atomic.Bool
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	a := &fakeAgent{t: t, hits: map[string]models.CommandOutcomeResponse{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/command-outcome", func(w http.ResponseWriter, r *http.Request) {
		a.calls.Add(1)
		if a.failNext.Swap(false) {
			// Hijack the connection to simulate a network drop.
			hj, ok := w.(http.Hijacker)
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		id := r.URL.Query().Get("request_id")
		out, ok := a.hits[id]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown request_id"})
			return
		}
		out.RequestID = id
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	a.server = httptest.NewServer(mux)
	t.Cleanup(a.server.Close)
	return a
}

func (a *fakeAgent) addr() string {
	return strings.TrimPrefix(a.server.URL, "http://")
}

// reconcilerTestSetup wires a Store with a single host pointing at
// the fakeAgent, plus one pending command-log entry awaiting outcome
// reconciliation.
func reconcilerTestSetup(t *testing.T, requestID, action string) (*Store, *fakeAgent) {
	t.Helper()
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	s.hub = NewSSEHub()

	a := newFakeAgent(t)
	addr := a.addr()
	port := intFromAddr(addr)
	host := strings.Split(addr, ":")[0]
	s.hosts["hv1"] = &models.Host{ID: "hv1", IP: host, AgentPort: port, LastSeen: time.Now()}

	s.commandLog.Record(&models.CommandLogEntry{
		RequestID:   requestID,
		Action:      action,
		HostID:      "hv1",
		VMUUID:      "vm-uuid-1",
		VMName:      "test-vm",
		Outcome:     "pending",
		SubmittedAt: time.Now().Add(-time.Minute),
	})
	s.commandLog.MarkPending(requestID, time.Now().Add(outcomeReconcileWindow), "agent call timed out")
	return s, a
}

// TestReconciler_RecoversFromHTTPTimeout: the canonical happy path —
// the entry was timed-out on /command, the agent's cache holds a
// successful outcome, and the reconciler transitions pending →
// success.
func TestReconciler_RecoversFromHTTPTimeout(t *testing.T) {
	s, a := reconcilerTestSetup(t, "req_recover", "DELETE_SNAPSHOT")
	a.hits["req_recover"] = models.CommandOutcomeResponse{
		Outcome:    "success",
		StatusCode: 200,
		Message:    "snapshot deleted",
	}

	reconcilerOnce(context.Background(), s, &http.Client{Timeout: reconcilerHTTPTimeout})

	got := s.commandLog.List(0)[0]
	if got.Outcome != "success" {
		t.Fatalf("outcome = %q, want success", got.Outcome)
	}
	if got.Message != "snapshot deleted" {
		t.Fatalf("message = %q", got.Message)
	}
	if got.AppliedAt == nil {
		t.Fatal("AppliedAt should be stamped on terminal entry")
	}
	if got.ExpiresAt == nil {
		t.Fatal("ExpiresAt should be stamped on terminal entry")
	}
}

// TestReconciler_AgentReturnsFailed: failed outcome propagates with
// the agent's error string.
func TestReconciler_AgentReturnsFailed(t *testing.T) {
	s, a := reconcilerTestSetup(t, "req_fail", "MIGRATE")
	a.hits["req_fail"] = models.CommandOutcomeResponse{
		Outcome:    "failed",
		StatusCode: 500,
		Error:      "virsh migrate exit 1",
	}

	reconcilerOnce(context.Background(), s, &http.Client{Timeout: reconcilerHTTPTimeout})

	got := s.commandLog.List(0)[0]
	if got.Outcome != "failed" {
		t.Fatalf("outcome = %q, want failed", got.Outcome)
	}
	if got.Message != "virsh migrate exit 1" {
		t.Fatalf("message = %q (want agent error)", got.Message)
	}
}

// TestReconciler_Agent404FallsBackToTimer: pre-5.47.2 agent (or
// cache-evicted entry) returns 404. Reconciler must NOT regress the
// entry to a verdict — it stays pending until InflightUntil expires.
func TestReconciler_Agent404FallsBackToTimer(t *testing.T) {
	s, a := reconcilerTestSetup(t, "req_404", "STOP")
	// Don't register the request_id in agent's hits map → 404.

	beforePolls := a.calls.Load()
	reconcilerOnce(context.Background(), s, &http.Client{Timeout: reconcilerHTTPTimeout})
	if a.calls.Load() <= beforePolls {
		t.Fatal("reconciler did not poll agent")
	}

	got := s.commandLog.List(0)[0]
	if got.Outcome != "pending" {
		t.Fatalf("outcome = %q, want pending (404 must not lock in a verdict)", got.Outcome)
	}
	if got.LastPolledAt.IsZero() {
		t.Fatal("LastPolledAt should be bumped after a 404 poll")
	}
}

// TestReconciler_AgentUnreachable: agent connection drops mid-poll.
// Entry stays pending; LastPolledAt bumped so we don't hammer.
func TestReconciler_AgentUnreachable(t *testing.T) {
	s, a := reconcilerTestSetup(t, "req_drop", "RESTART")
	a.failNext.Store(true)

	reconcilerOnce(context.Background(), s, &http.Client{Timeout: reconcilerHTTPTimeout})

	got := s.commandLog.List(0)[0]
	if got.Outcome != "pending" {
		t.Fatalf("outcome = %q, want pending after transport error", got.Outcome)
	}
	if got.LastPolledAt.IsZero() {
		t.Fatal("LastPolledAt should be bumped even on transport error")
	}
}

// TestReconciler_RateLimitsPerEntry: a recently-polled entry is not
// re-polled within reconcilerPollInterval.
func TestReconciler_RateLimitsPerEntry(t *testing.T) {
	s, a := reconcilerTestSetup(t, "req_rate", "STOP")
	// Stamp LastPolledAt as just-now so the entry is rate-limited.
	s.commandLog.RecordPoll("req_rate", time.Now())

	reconcilerOnce(context.Background(), s, &http.Client{Timeout: reconcilerHTTPTimeout})
	if a.calls.Load() != 0 {
		t.Fatalf("agent polled %d times; rate-limit should have skipped it", a.calls.Load())
	}
}

// TestReconciler_HostRemovedMarksUnknown: the host was deleted from
// inventory while the command was pending. Reconciler can't poll
// anyone, so it marks the entry "unknown" rather than leaving it
// pending forever.
func TestReconciler_HostRemovedMarksUnknown(t *testing.T) {
	s, _ := reconcilerTestSetup(t, "req_orphan", "START")
	delete(s.hosts, "hv1")

	reconcilerOnce(context.Background(), s, &http.Client{Timeout: reconcilerHTTPTimeout})

	got := s.commandLog.List(0)[0]
	if got.Outcome != "unknown" {
		t.Fatalf("outcome = %q, want unknown", got.Outcome)
	}
	if !strings.Contains(got.Message, "host removed") {
		t.Fatalf("message should mention host removal, got %q", got.Message)
	}
}

// TestReconciler_SkipsEntriesWithoutInflightUntil: a pre-5.47.2
// pending entry (no InflightUntil) must not be touched by the live
// reconciler — that's the boot/stranded sweep's job.
func TestReconciler_SkipsEntriesWithoutInflightUntil(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	s.hub = NewSSEHub()
	a := newFakeAgent(t)
	port := intFromAddr(a.addr())
	s.hosts["hv1"] = &models.Host{ID: "hv1", IP: strings.Split(a.addr(), ":")[0], AgentPort: port, LastSeen: time.Now()}
	s.commandLog.Record(&models.CommandLogEntry{
		RequestID:   "req_legacy",
		Action:      "START",
		HostID:      "hv1",
		Outcome:     "pending",
		SubmittedAt: time.Now().Add(-time.Hour),
		// No InflightUntil — pre-5.47.2 entry.
	})

	reconcilerOnce(context.Background(), s, &http.Client{Timeout: reconcilerHTTPTimeout})

	if a.calls.Load() != 0 {
		t.Fatalf("agent polled %d times; legacy pending entry must be left alone", a.calls.Load())
	}
}

// TestReconciler_SkipsEntriesPastDeadline: an entry whose
// InflightUntil deadline has already passed is the stranded sweep's
// job, not the live reconciler's.
func TestReconciler_SkipsEntriesPastDeadline(t *testing.T) {
	s, a := reconcilerTestSetup(t, "req_late", "STOP")
	// Force the deadline into the past.
	s.commandLog.mu.Lock()
	s.commandLog.entries["req_late"].InflightUntil = time.Now().Add(-time.Minute)
	s.commandLog.mu.Unlock()

	reconcilerOnce(context.Background(), s, &http.Client{Timeout: reconcilerHTTPTimeout})

	if a.calls.Load() != 0 {
		t.Fatalf("agent polled %d times; expired entry should fall to stranded sweep", a.calls.Load())
	}
}

// TestReconciler_StopsOnContextCancel: cancelling the parent context
// returns from runCommandReconciler promptly. Belt-and-braces for
// graceful shutdown wired into serverCtx.
func TestReconciler_StopsOnContextCancel(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	s.hub = NewSSEHub()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runCommandReconciler(ctx, s)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runCommandReconciler did not return after ctx cancel")
	}
}
