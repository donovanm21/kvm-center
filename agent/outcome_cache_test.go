package main

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"
)

// TestOutcomeCache_RoundTrip: put + get returns the same body.
func TestOutcomeCache_RoundTrip(t *testing.T) {
	c := &outcomeCache{entries: make(map[string]cachedOutcome)}
	c.put("r1", 200, []byte(`{"ok":true}`))
	got, ok := c.get("r1")
	if !ok {
		t.Fatal("cache miss after put")
	}
	if !bytes.Equal(got.body, []byte(`{"ok":true}`)) {
		t.Fatalf("body mismatch: %s", got.body)
	}
	if got.status != 200 {
		t.Fatalf("status mismatch: %d", got.status)
	}
}

// TestOutcomeCache_TTLExpiry: entries past outcomeCacheTTL are
// treated as misses.
func TestOutcomeCache_TTLExpiry(t *testing.T) {
	c := &outcomeCache{entries: make(map[string]cachedOutcome)}
	c.entries["old"] = cachedOutcome{
		status: 200,
		body:   []byte("x"),
		at:     time.Now().Add(-2 * outcomeCacheTTL),
	}
	if _, ok := c.get("old"); ok {
		t.Fatal("expired entry returned as hit")
	}
	// and it should be evicted from the map.
	if _, still := c.entries["old"]; still {
		t.Fatal("expired entry not removed from cache")
	}
}

// TestOutcomeCache_EmptyIDNoOp: empty request_id is never stored
// (pre-5.32.17 servers + direct curl).
func TestOutcomeCache_EmptyIDNoOp(t *testing.T) {
	c := &outcomeCache{entries: make(map[string]cachedOutcome)}
	c.put("", 200, []byte("x"))
	if len(c.entries) != 0 {
		t.Fatal("empty request_id wrote to cache")
	}
	if _, ok := c.get(""); ok {
		t.Fatal("empty request_id returned hit")
	}
}

// TestRecorderWriter_CapturesStatusAndBody: the recorder mirrors
// everything written through it.
func TestRecorderWriter_CapturesStatusAndBody(t *testing.T) {
	inner := httptest.NewRecorder()
	rec := &recorderWriter{ResponseWriter: inner}
	rec.WriteHeader(201)
	_, _ = rec.Write([]byte(`{"msg":"created"}`))
	if rec.status != 201 {
		t.Fatalf("status captured = %d", rec.status)
	}
	if string(rec.body) != `{"msg":"created"}` {
		t.Fatalf("body captured = %q", rec.body)
	}
	// Inner writer also saw both.
	if inner.Code != 201 {
		t.Fatalf("inner status = %d", inner.Code)
	}
	if inner.Body.String() != `{"msg":"created"}` {
		t.Fatalf("inner body = %q", inner.Body.String())
	}
}
