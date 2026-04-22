package main

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// TestEvaluate_EdgeTransitions locks the one-shot warning behaviour:
// each (kind, identity, severity) fires exactly once on entry into
// its threshold band, and re-arms after the cert leaves the band.
func TestEvaluate_EdgeTransitions(t *testing.T) {
	state := newCertWatcherState()
	var fires []string
	record := func(tag string) func(sev string) {
		return func(sev string) { fires = append(fires, tag+"/"+sev) }
	}

	// Healthy cert — no fire.
	evaluate(state, "server-tls", "CN=a", 60, 30, 7, record("a"))
	if len(fires) != 0 {
		t.Fatalf("healthy cert fired: %v", fires)
	}

	// Crosses into warning — one fire.
	evaluate(state, "server-tls", "CN=a", 20, 30, 7, record("a"))
	if len(fires) != 1 || fires[0] != "a/warning" {
		t.Fatalf("warn-entry fires = %v", fires)
	}

	// Still in warning — no new fire.
	evaluate(state, "server-tls", "CN=a", 15, 30, 7, record("a"))
	if len(fires) != 1 {
		t.Fatalf("warn-steady fired again: %v", fires)
	}

	// Crosses into critical — one new fire.
	evaluate(state, "server-tls", "CN=a", 3, 30, 7, record("a"))
	if len(fires) != 2 || fires[1] != "a/critical" {
		t.Fatalf("crit-entry fires = %v", fires)
	}

	// Still critical — no new fire.
	evaluate(state, "server-tls", "CN=a", 1, 30, 7, record("a"))
	if len(fires) != 2 {
		t.Fatalf("crit-steady fired again: %v", fires)
	}

	// Cert rotated past both thresholds — state clears.
	evaluate(state, "server-tls", "CN=a", 90, 30, 7, record("a"))
	if len(fires) != 2 {
		t.Fatalf("healthy re-entry fired: %v", fires)
	}

	// Drifts back into warning — should fire again (re-armed).
	evaluate(state, "server-tls", "CN=a", 10, 30, 7, record("a"))
	if len(fires) != 3 || fires[2] != "a/warning" {
		t.Fatalf("re-armed warn fires = %v", fires)
	}
}

// TestEvaluate_PerIdentity: two certs of the same kind track state
// independently.
func TestEvaluate_PerIdentity(t *testing.T) {
	state := newCertWatcherState()
	var fires []string
	record := func(tag string) func(sev string) {
		return func(sev string) { fires = append(fires, tag+"/"+sev) }
	}

	evaluate(state, "hypervisor-cert", "hvnode01", 20, 30, 7, record("hv01"))
	evaluate(state, "hypervisor-cert", "hvnode02", 60, 30, 7, record("hv02"))
	if len(fires) != 1 || fires[0] != "hv01/warning" {
		t.Fatalf("per-identity fires = %v", fires)
	}

	// hvnode01 stays warn, hvnode02 enters critical.
	evaluate(state, "hypervisor-cert", "hvnode01", 20, 30, 7, record("hv01"))
	evaluate(state, "hypervisor-cert", "hvnode02", 5, 30, 7, record("hv02"))
	if len(fires) != 2 || fires[1] != "hv02/critical" {
		t.Fatalf("independent transitions fires = %v", fires)
	}
}

func TestDaysUntil_Negative(t *testing.T) {
	now := mustTime(t, "2026-04-21T00:00:00Z")
	past := mustTime(t, "2026-04-01T00:00:00Z")
	if d := daysUntil(past, now); d >= 0 {
		t.Fatalf("expired cert should report negative days, got %d", d)
	}
}
