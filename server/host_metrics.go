package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/models"
)

// hostMetricRing is a fixed-size circular buffer of the last
// hostMetricCap samples per host. At the default 30 s telemetry
// interval that's a 1 h window, which is the glance scope the in-UI
// sparklines target. Optional persisted history uses a separate bounded
// store and API; external metric sinks remain available.
const hostMetricCap = 120

type hostMetricSample struct {
	T       time.Time `json:"t"`
	CPU     float64   `json:"cpu"`
	Mem     float64   `json:"mem"`
	Storage float64   `json:"storage"` // % allocated across all pools on the host
	VMs     int       `json:"vms"`
}

type hostMetricRing struct {
	mu   sync.RWMutex
	buf  [hostMetricCap]hostMetricSample
	head int  // next write slot
	full bool // buffer has wrapped at least once
}

func (r *hostMetricRing) append(s hostMetricSample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.head] = s
	r.head = (r.head + 1) % hostMetricCap
	if r.head == 0 {
		r.full = true
	}
}

// snapshot returns samples in chronological order (oldest → newest).
// Callers get a fresh slice, safe to mutate.
func (r *hostMetricRing) snapshot() []hostMetricSample {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var n, start int
	if r.full {
		n = hostMetricCap
		start = r.head
	} else {
		n = r.head
		start = 0
	}
	out := make([]hostMetricSample, n)
	for i := 0; i < n; i++ {
		out[i] = r.buf[(start+i)%hostMetricCap]
	}
	return out
}

// AppendHostMetric pushes a fresh sample onto the per-host ring. Called
// from ApplyTelemetry after the host + storage maps have been updated;
// derives storage% from the current pools on the host.
func (s *Store) AppendHostMetric(hostID string, h *models.Host, pools []*models.StoragePool) {
	s.appendHostMetricAt(hostID, h, pools, time.Now())
}

func (s *Store) appendHostMetricAt(hostID string, h *models.Host, pools []*models.StoragePool, observedAt time.Time) {
	if h == nil {
		return
	}
	var cap, alloc float64
	for _, p := range pools {
		if p == nil {
			continue
		}
		cap += p.CapacityGB
		alloc += p.AllocationGB
	}
	var storagePct float64
	if cap > 0 {
		storagePct = (alloc / cap) * 100
	}
	sample := hostMetricSample{
		T:       h.LastSeen,
		CPU:     h.CPUUsage,
		Mem:     h.MemUsage,
		Storage: storagePct,
		VMs:     h.VMCount,
	}
	s.mu.Lock()
	ring, ok := s.hostMetrics[hostID]
	if !ok {
		ring = &hostMetricRing{}
		s.hostMetrics[hostID] = ring
	}
	s.mu.Unlock()
	ring.append(sample)
	s.recordHistory(hostID, observedAt, h.CPUUsage, h.MemUsage, storagePct, h.VMCount)
}

// HostMetricsSnapshot returns the ring buffer for a host in chronological
// order. Empty slice if the host has no samples yet.
func (s *Store) HostMetricsSnapshot(hostID string) []hostMetricSample {
	s.mu.RLock()
	ring, ok := s.hostMetrics[hostID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return ring.snapshot()
}

// handleHostMetrics serves GET /api/hosts/<id>/metrics. Host ID lookup
// mirrors the short-vs-FQDN fallback used elsewhere (v5.10.3 gotcha).
func handleHostMetrics(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		// Path: /api/hosts/<id>/metrics
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 4 {
			writeError(w, http.StatusBadRequest, "host id required")
			return
		}
		id := parts[2]
		samples := store.HostMetricsSnapshot(id)
		if samples == nil {
			// Try FQDN fallback (hv01 vs hv01.cluster.local).
			for _, h := range store.GetHosts() {
				if strings.EqualFold(h.ID, id) ||
					strings.EqualFold(strings.SplitN(h.ID, ".", 2)[0], id) {
					samples = store.HostMetricsSnapshot(h.ID)
					break
				}
			}
		}
		if samples == nil {
			samples = []hostMetricSample{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"host_id":      id,
			"window_cap":   hostMetricCap,
			"interval_sec": 30,
			"samples":      samples,
		})
	}
}
