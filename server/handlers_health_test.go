package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// TestRollup_WorstOf: rollup returns the worst status across
// subsystems. Red dominates amber dominates green.
func TestRollup_WorstOf(t *testing.T) {
	cases := []struct {
		name string
		in   []SubsystemHealth
		want string
	}{
		{"all green", []SubsystemHealth{{Status: "green"}, {Status: "green"}}, "green"},
		{"one amber", []SubsystemHealth{{Status: "green"}, {Status: "amber"}, {Status: "green"}}, "amber"},
		{"red dominates", []SubsystemHealth{{Status: "amber"}, {Status: "red"}, {Status: "green"}}, "red"},
		{"empty", []SubsystemHealth{}, "green"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rollup(tc.in...); got != tc.want {
				t.Fatalf("rollup = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCheckHypervisors_OfflineTriggersRed: one offline host pushes
// the subsystem to red even when others are fine.
func TestCheckHypervisors_OfflineTriggersRed(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	s.hosts["h1"] = &models.Host{ID: "h1", Status: models.HostOnline, LastSeen: time.Now(), AgentVersion: Version}
	s.hosts["h2"] = &models.Host{ID: "h2", Status: models.HostOffline, LastSeen: time.Now().Add(-time.Hour), AgentVersion: Version}
	got := checkHypervisors(s)
	if got.Status != healthStatusRed {
		t.Fatalf("offline host produced %s, want red", got.Status)
	}
}

// TestCheckHypervisors_OutdatedAgentAmber: all online but one on
// an old version → amber.
func TestCheckHypervisors_OutdatedAgentAmber(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	s.hosts["h1"] = &models.Host{ID: "h1", Status: models.HostOnline, LastSeen: time.Now(), AgentVersion: Version}
	s.hosts["h2"] = &models.Host{ID: "h2", Status: models.HostOnline, LastSeen: time.Now(), AgentVersion: "v0.0.1"}
	got := checkHypervisors(s)
	if got.Status != healthStatusAmber {
		t.Fatalf("outdated agent produced %s, want amber", got.Status)
	}
}

// TestCheckCommandLog_UnknownAmber: an "unknown" entry flags amber.
func TestCheckCommandLog_UnknownAmber(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	s.commandLog.Record(&models.CommandLogEntry{RequestID: "stuck", Outcome: "unknown", SubmittedAt: time.Now()})
	s.commandLog.Record(&models.CommandLogEntry{RequestID: "ok", Outcome: "success", SubmittedAt: time.Now()})
	got := checkCommandLog(s)
	if got.Status != healthStatusAmber {
		t.Fatalf("unknown entry produced %s, want amber", got.Status)
	}
}

// TestHandleSystemHealth_CachesResponse: two back-to-back GETs
// return the same snapshot (proves the 15s cache works).
func TestHandleSystemHealth_CachesResponse(t *testing.T) {
	healthCache.mu.Lock()
	healthCache.value = nil
	healthCache.computedAt = time.Time{}
	healthCache.mu.Unlock()
	s := NewStore(t.TempDir() + "/data.json")
	handler := handleSystemHealth(s, nil, nil, nil)

	var h1 SystemHealth
	r1 := httptest.NewRecorder()
	handler(r1, httptest.NewRequest(http.MethodGet, "/api/admin/system-health", nil))
	if r1.Code != http.StatusOK {
		t.Fatalf("first call status %d", r1.Code)
	}
	_ = json.Unmarshal(r1.Body.Bytes(), &h1)

	var h2 SystemHealth
	r2 := httptest.NewRecorder()
	handler(r2, httptest.NewRequest(http.MethodGet, "/api/admin/system-health", nil))
	_ = json.Unmarshal(r2.Body.Bytes(), &h2)

	if !h1.GeneratedAt.Equal(h2.GeneratedAt) {
		t.Fatalf("cache missed between back-to-back calls: %s != %s", h1.GeneratedAt, h2.GeneratedAt)
	}
}
