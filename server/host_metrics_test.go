package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// Per-host metrics ring (v5.17). In-memory only; rebuilds after
// restart. Tests cover:
//
//   - Ring correctly grows until it wraps, then discards the oldest.
//   - Snapshot order is always chronological regardless of where the
//     head is within the underlying fixed-size buffer.
//   - Storage % is computed as sum(allocation)/sum(capacity) across
//     every pool, not averaged per-pool — one full pool on a host
//     that also has an empty pool still shows the weighted total.
//   - The GET /api/hosts/<id>/metrics handler returns the expected
//     envelope and 405s non-GETs.

func TestHostMetricRing_WrapsAndKeepsOrder(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	host := &models.Host{ID: "hv01", LastSeen: time.Now(), VMCount: 1}

	// Push 1.5x capacity so the ring has wrapped at least once.
	for i := 0; i < hostMetricCap+hostMetricCap/2; i++ {
		host.CPUUsage = float64(i)
		host.MemUsage = float64(i) * 2
		host.LastSeen = time.Unix(int64(1000+i), 0).UTC()
		s.AppendHostMetric("hv01", host, nil)
	}
	got := s.HostMetricsSnapshot("hv01")
	if len(got) != hostMetricCap {
		t.Fatalf("snapshot len=%d, want %d", len(got), hostMetricCap)
	}
	// The oldest retained sample should be index (total - cap).
	wantFirst := float64(hostMetricCap / 2)
	if got[0].CPU != wantFirst {
		t.Errorf("first retained sample CPU=%v, want %v", got[0].CPU, wantFirst)
	}
	// And the newest should reflect the last push.
	wantLast := float64(hostMetricCap + hostMetricCap/2 - 1)
	if got[len(got)-1].CPU != wantLast {
		t.Errorf("last sample CPU=%v, want %v", got[len(got)-1].CPU, wantLast)
	}
	// Chronological: timestamps monotonically increase.
	for i := 1; i < len(got); i++ {
		if !got[i].T.After(got[i-1].T) {
			t.Errorf("sample %d not after %d (%v <= %v)", i, i-1, got[i].T, got[i-1].T)
		}
	}
}

func TestHostMetricRing_StoragePercentIsWeighted(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	host := &models.Host{ID: "hv01", LastSeen: time.Now()}
	pools := []*models.StoragePool{
		// 100 GB cap / 80 GB used + 100 GB cap / 0 used → 80/200 = 40 %
		{CapacityGB: 100, AllocationGB: 80},
		{CapacityGB: 100, AllocationGB: 0},
	}
	s.AppendHostMetric("hv01", host, pools)
	snap := s.HostMetricsSnapshot("hv01")
	if len(snap) != 1 {
		t.Fatalf("len=%d, want 1", len(snap))
	}
	if snap[0].Storage != 40 {
		t.Errorf("Storage=%v, want 40", snap[0].Storage)
	}
}

func TestHostMetricRing_IgnoresNilHost(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	s.AppendHostMetric("hv01", nil, nil) // must not panic
	if got := s.HostMetricsSnapshot("hv01"); len(got) != 0 {
		t.Errorf("expected no samples, got %d", len(got))
	}
}

func TestHostMetrics_Handler(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	host := &models.Host{ID: "hv01", LastSeen: time.Now(), CPUUsage: 25, MemUsage: 40}
	s.AppendHostMetric("hv01", host, nil)

	rr := httptest.NewRecorder()
	handleHostMetrics(s)(rr, httptest.NewRequest("GET", "/api/hosts/hv01/metrics", nil))
	if rr.Code != 200 {
		t.Fatalf("got %d, want 200", rr.Code)
	}
	var envelope struct {
		HostID      string             `json:"host_id"`
		WindowCap   int                `json:"window_cap"`
		IntervalSec int                `json:"interval_sec"`
		Samples     []hostMetricSample `json:"samples"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.HostID != "hv01" {
		t.Errorf("host_id=%q, want hv01", envelope.HostID)
	}
	if envelope.WindowCap != hostMetricCap {
		t.Errorf("window_cap=%d, want %d", envelope.WindowCap, hostMetricCap)
	}
	if envelope.IntervalSec != 30 {
		t.Errorf("interval_sec=%d, want 30", envelope.IntervalSec)
	}
	if len(envelope.Samples) != 1 || envelope.Samples[0].CPU != 25 {
		t.Errorf("unexpected samples: %+v", envelope.Samples)
	}
}

func TestHostMetrics_Handler_405OnPost(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	rr := httptest.NewRecorder()
	handleHostMetrics(s)(rr, httptest.NewRequest("POST", "/api/hosts/hv01/metrics", nil))
	if rr.Code != 405 {
		t.Errorf("got %d, want 405", rr.Code)
	}
}

func TestHostMetrics_Handler_EmptyWhenUnknown(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	rr := httptest.NewRecorder()
	handleHostMetrics(s)(rr, httptest.NewRequest("GET", "/api/hosts/nope/metrics", nil))
	if rr.Code != 200 {
		t.Fatalf("got %d, want 200", rr.Code)
	}
	var env struct {
		Samples []hostMetricSample `json:"samples"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Samples == nil || len(env.Samples) != 0 {
		t.Errorf("expected [], got %v", env.Samples)
	}
}
