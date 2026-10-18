package main

import (
	"net/http"
	"strings"
	"time"
)

type historyViewPoint struct {
	T          int64   `json:"t"`
	N          uint32  `json:"n"`
	CPUAvg     float64 `json:"cpu_avg"`
	CPUMax     float64 `json:"cpu_max"`
	MemAvg     float64 `json:"mem_avg"`
	MemMax     float64 `json:"mem_max"`
	StorageAvg float64 `json:"storage_avg"`
	StorageMax float64 `json:"storage_max"`
	VMs        int     `json:"vms"`
}

func handleHostHistory(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, 405, "GET only")
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/hosts/"), "/history")
		if !validHistoryID(id) {
			writeError(w, 400, "invalid host id")
			return
		}
		source := r.URL.Query().Get("source")
		if len(r.URL.Query()["source"]) > 1 || (source != "" && source != "current" && source != "legacy") {
			writeError(w, 400, "source must be current or legacy")
			return
		}
		if source == "legacy" {
			if r.URL.Query().Has("range") || r.URL.Query().Has("tier") {
				writeError(w, 400, "legacy history returns all retained tiers; range and tier are unsupported")
				return
			}
			s.mu.RLock()
			state := s.legacyHistory.Clone()
			s.mu.RUnlock()
			if state == nil || state.Records[id] == nil {
				writeError(w, 404, "legacy history not found")
				return
			}
			view, err := state.View(id)
			if err != nil {
				writeError(w, 409, "legacy history validation failed")
				return
			}
			writeJSON(w, 200, map[string]any{"source": "legacy", "read_only": true, "source_contract": view.SourceContract, "host": view.Host, "original_sha256": view.OriginalSHA256, "timestamp_unit": view.TimestampUnit, "metric_unit": view.MetricUnit, "vm_count_meaning": view.VMCountMeaning, "retention_meaning": view.RetentionMeaning, "tiers": view.Tiers})
			return
		}
		if s.GetHost(id) == nil {
			writeError(w, 404, "host not found")
			return
		}
		hours := int64(48)
		switch r.URL.Query().Get("range") {
		case "", "48h":
		case "1h":
			hours = 1
		case "6h":
			hours = 6
		case "24h":
			hours = 24
		default:
			writeError(w, 400, "range must be 1h, 6h, 24h or 48h")
			return
		}
		now := time.Now().Unix()
		cut := now - now%historyBucketSeconds - hours*3600 + historyBucketSeconds
		points := []historyViewPoint{}
		active := 0
		var last int64
		tracked := false
		s.mu.RLock()
		enabled := s.historyEnabled
		if s.history != nil {
			for _, h := range s.history.Hosts {
				if h.Last >= historyCutoff(now) {
					active++
				}
			}
			if h, ok := s.history.Hosts[id]; ok && h.Last >= historyCutoff(now) {
				tracked = true
				last = h.Last
				for _, p := range h.Points {
					if p.T < cut || p.T > now || (h.Last > now && p.T == h.Points[len(h.Points)-1].T) {
						continue
					}
					scale := float64(historyScale)
					denom := scale * float64(p.N)
					points = append(points, historyViewPoint{p.T, p.N, float64(p.CPU) / denom, float64(p.CPUMax) / scale, float64(p.Mem) / denom, float64(p.MemMax) / scale, float64(p.Storage) / denom, float64(p.StorageMax) / scale, p.VMs})
				}
			}
		}
		s.mu.RUnlock()
		writeJSON(w, 200, map[string]any{"host": id, "tier": "5m", "range_hours": hours, "bucket_seconds": historyBucketSeconds, "retention_buckets": historyMaxPoints, "retention_start": historyCutoff(now), "collection_enabled": enabled, "tracked": tracked, "active_hosts": active, "max_hosts": historyMaxHosts, "capacity_full": active >= historyMaxHosts, "last_sample": last, "clock_ahead": last > now, "points": points})
	}
}
