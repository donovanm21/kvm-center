package main

// Tests for handleCommandOutcome (v5.47.2). The agent endpoint must:
//   1. return 200 + structured outcome for cache hits,
//   2. return 404 for unknown request_ids,
//   3. return 405 for non-GET,
//   4. return 400 when request_id is missing.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func TestCommandOutcome_HitSuccess(t *testing.T) {
	resetOutcomes(t)
	outcomes.put("req_ok", 200, []byte(`{"success":true,"message":"VM started"}`))

	rr := doGet(t, "/command-outcome?request_id=req_ok")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got models.CommandOutcomeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v / %s", err, rr.Body.String())
	}
	if got.RequestID != "req_ok" || got.Outcome != "success" || got.StatusCode != 200 {
		t.Fatalf("got = %+v", got)
	}
	if got.Message != "VM started" {
		t.Fatalf("message = %q", got.Message)
	}
	if got.CachedAt.IsZero() {
		t.Fatal("cached_at not set")
	}
}

func TestCommandOutcome_HitFailed(t *testing.T) {
	resetOutcomes(t)
	outcomes.put("req_fail", 500, []byte(`{"success":false,"message":"virsh exit 1"}`))

	rr := doGet(t, "/command-outcome?request_id=req_fail")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got models.CommandOutcomeResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Outcome != "failed" {
		t.Fatalf("outcome = %q, want failed", got.Outcome)
	}
	if got.Error == "" {
		t.Fatal("error must be populated for failed outcomes")
	}
}

func TestCommandOutcome_MissReturns404(t *testing.T) {
	resetOutcomes(t)
	rr := doGet(t, "/command-outcome?request_id=does-not-exist")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	var got map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got["error"] != "unknown request_id" {
		t.Fatalf("body = %s", rr.Body.String())
	}
	if got["request_id"] != "does-not-exist" {
		t.Fatalf("request_id echo = %s", got["request_id"])
	}
}

func TestCommandOutcome_MissingIDReturns400(t *testing.T) {
	resetOutcomes(t)
	rr := doGet(t, "/command-outcome")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCommandOutcome_RejectsNonGET(t *testing.T) {
	resetOutcomes(t)
	req := httptest.NewRequest(http.MethodPost, "/command-outcome?request_id=x", nil)
	rr := httptest.NewRecorder()
	handleCommandOutcome(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestCommandOutcome_TTLExpiredEntry(t *testing.T) {
	resetOutcomes(t)
	outcomes.entries["stale"] = cachedOutcome{
		status: 200,
		body:   []byte(`{"success":true}`),
		at:     time.Now().Add(-2 * outcomeCacheTTL),
	}
	rr := doGet(t, "/command-outcome?request_id=stale")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expired entry returned %d, want 404", rr.Code)
	}
}

// resetOutcomes clears the package-global outcome cache between
// tests so they don't leak state into each other.
func resetOutcomes(t *testing.T) {
	t.Helper()
	outcomes.mu.Lock()
	outcomes.entries = make(map[string]cachedOutcome)
	outcomes.mu.Unlock()
}

func doGet(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rr := httptest.NewRecorder()
	handleCommandOutcome(rr, req)
	return rr
}
