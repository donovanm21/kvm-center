package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/models"
)

// InfluxDB v2 metrics sink (v5.13.0). Same shape as audit_shipping —
// a buffered channel feeding a background worker that batches and
// POSTs. Disabled by default; enabling it does not gate the UI
// path, so a broken/down Influx never slows down the control plane.
//
// Line protocol reference:
//   measurement,tag=val field=value timestamp_ns
// We write two measurements by default:
//   <prefix>host  — per-host telemetry tick
//   <prefix>vm    — per-VM sample
// Migration terminal events and grant outcomes land under
// <prefix>migration / <prefix>grant (v5.13.1 wires the emitters).

const (
	metricsQueueSize        = 512
	metricsDefaultBatchSize = 100
	metricsDefaultFlushSec  = 10
	metricsDefaultPrefix    = "kvm_center_"
	// Circuit breaker (P2 #4, v5.37.0). After this many consecutive
	// push failures the worker stops draining the queue for the
	// cooldown window — telemetry writers continue producing
	// samples; queueSample drops them on a full channel as before.
	// Influx coming back is detected by a successful push; failure
	// counter resets on success.
	metricsCircuitFailThreshold = 6
	metricsCircuitCooldown      = 90 * time.Second
)

// metricSample is one line-protocol record ready to format.
type metricSample struct {
	Measurement string // e.g. "host"
	Tags        map[string]string
	Fields      map[string]any
	Timestamp   time.Time
}

// startMetricsWorker wires the store-level queue + worker. Safe to
// call when MetricsConfig is nil or disabled — the worker no-ops
// until the operator saves a config via Settings → Metrics.
func (s *Store) startMetricsWorker(ctx context.Context) {
	s.mu.Lock()
	if s.metricsQueue == nil {
		s.metricsQueue = make(chan metricSample, metricsQueueSize)
	}
	s.mu.Unlock()

	go func() {
		batch := make([]metricSample, 0, metricsDefaultBatchSize*2)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		// Circuit-breaker state — see metricsCircuit* constants.
		consecutiveFails := 0
		var openUntil time.Time

		drain := func() {
			for {
				select {
				case m := <-s.metricsQueue:
					batch = append(batch, m)
				default:
					return
				}
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case m := <-s.metricsQueue:
				batch = append(batch, m)
				drain()
			case <-ticker.C:
			}
			cfg := s.metricsConfigSnapshot()
			if cfg == nil || !cfg.Enabled || cfg.URL == "" || cfg.Bucket == "" || cfg.Org == "" {
				if len(batch) > 0 {
					batch = batch[:0]
				}
				continue
			}
			size := cfg.BatchSize
			if size <= 0 {
				size = metricsDefaultBatchSize
			}
			flush := cfg.FlushSeconds
			if flush <= 0 {
				flush = metricsDefaultFlushSec
			}
			if len(batch) < size && !(len(batch) > 0 && time.Since(batch[0].Timestamp) >= time.Duration(flush)*time.Second) {
				continue
			}
			// Circuit open → drop the batch without dialing Influx.
			// Producers stay non-blocking (queueSample drops on full),
			// so this just stops us hammering an unreachable server.
			if !openUntil.IsZero() && time.Now().Before(openUntil) {
				batch = batch[:0]
				continue
			}
			if err := pushToInflux(cfg, batch); err != nil {
				s.recordMetricsResult(err, int64(len(batch)))
				consecutiveFails++
				log.Printf("metrics: influx push failed: %v (batch=%d, consecutive=%d)", err, len(batch), consecutiveFails)
				if consecutiveFails >= metricsCircuitFailThreshold {
					openUntil = time.Now().Add(metricsCircuitCooldown)
					log.Printf("metrics: circuit opened for %s after %d consecutive failures", metricsCircuitCooldown, consecutiveFails)
					batch = batch[:0]
				} else if len(batch) > metricsQueueSize*2 {
					log.Printf("metrics: dropping %d samples after sustained failure", len(batch))
					batch = batch[:0]
				}
				continue
			}
			if consecutiveFails > 0 {
				log.Printf("metrics: influx recovered after %d failures", consecutiveFails)
			}
			consecutiveFails = 0
			openUntil = time.Time{}
			s.recordMetricsResult(nil, int64(len(batch)))
			batch = batch[:0]
		}
	}()
}

// queueSample is the non-blocking producer. Drops on a full queue so
// a stuck Influx never back-pressures the telemetry write path.
func (s *Store) queueSample(m metricSample) {
	if s.metricsQueue == nil {
		return
	}
	select {
	case s.metricsQueue <- m:
	default:
		// Full — drop silently. Worker logs sustained-failure drops.
	}
}

// EmitHostMetrics is called from ApplyTelemetry with the fresh host
// snapshot. Keep allocations tight — telemetry ticks fire every 30s
// per host.
func (s *Store) EmitHostMetrics(h *models.Host, prefix string) {
	if prefix == "" {
		prefix = metricsDefaultPrefix
	}
	s.queueSample(metricSample{
		Measurement: prefix + "host",
		Tags:        map[string]string{"host_id": h.ID, "status": string(h.Status)},
		Fields: map[string]any{
			"cpu_usage":    h.CPUUsage,
			"mem_usage":    h.MemUsage,
			"mem_total_mb": int64(h.MemTotalMB),
			"mem_free_mb":  int64(h.MemFreeMB),
			"vm_count":     int64(h.VMCount),
		},
		Timestamp: time.Now(),
	})
}

// EmitVMMetrics fans out per-VM samples. State is encoded as an int
// so Grafana can panel on it (running=1, paused=2, stopped=0, other=-1).
func (s *Store) EmitVMMetrics(vms []*models.VM, prefix string) {
	if prefix == "" {
		prefix = metricsDefaultPrefix
	}
	for _, v := range vms {
		s.queueSample(metricSample{
			Measurement: prefix + "vm",
			Tags: map[string]string{
				"host_id": v.HostID,
				"vm_uuid": v.UUID,
				"vm_name": v.Name,
				"state":   string(v.State),
			},
			Fields: map[string]any{
				"cpu_usage":      v.CPUUsage,
				"mem_usage_mb":   int64(v.MemUsageMB),
				"mem_total_mb":   int64(v.MemTotalMB),
				"state_code":     vmStateCode(v.State),
				"snapshot_count": int64(v.SnapshotCount),
			},
			Timestamp: time.Now(),
		})
	}
}

func vmStateCode(s models.VMState) int64 {
	switch s {
	case models.VMRunning:
		return 1
	case models.VMPaused:
		return 2
	case models.VMStopped:
		return 0
	case models.VMCrashed:
		return -2
	default:
		return -1
	}
}

// ── Config + worker plumbing ───────────────────────────────────────

func (s *Store) metricsConfigSnapshot() *models.MetricsConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.metrics == nil {
		return nil
	}
	cp := *s.metrics
	return &cp
}

func (s *Store) SetMetricsConfig(cfg *models.MetricsConfig) {
	s.mu.Lock()
	s.metrics = cfg
	s.mu.Unlock()
	s.AsyncSave()
}

func (s *Store) recordMetricsResult(err error, count int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metrics == nil {
		return
	}
	s.metrics.LastWriteAt = time.Now().UTC().Format(time.RFC3339)
	s.metrics.LastWriteCount = count
	if err != nil {
		s.metrics.LastWriteError = err.Error()
	} else {
		s.metrics.LastWriteError = ""
	}
}

// ── Line protocol + HTTP push ──────────────────────────────────────

// pushToInflux formats the batch as InfluxDB v2 line protocol and
// POSTs it to /api/v2/write with the operator's token.
func pushToInflux(cfg *models.MetricsConfig, batch []metricSample) error {
	if cfg.URL == "" || cfg.Org == "" || cfg.Bucket == "" {
		return errors.New("url / org / bucket required")
	}
	var body bytes.Buffer
	for _, m := range batch {
		writeLineProtocol(&body, m)
	}
	if body.Len() == 0 {
		return nil
	}
	endpoint := strings.TrimRight(cfg.URL, "/") +
		"/api/v2/write?org=" + escape(cfg.Org) +
		"&bucket=" + escape(cfg.Bucket) +
		"&precision=ns"
	req, err := http.NewRequest(http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Token "+cfg.Token)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("influx %d", resp.StatusCode)
	}
	return nil
}

// writeLineProtocol emits one line-protocol record. Tags sorted for
// deterministic wire output (makes diffing tests cheap).
func writeLineProtocol(w *bytes.Buffer, m metricSample) {
	w.WriteString(lpEscapeKey(m.Measurement))
	// Sort tag keys for stable output — tests diff against exact bytes.
	if len(m.Tags) > 0 {
		keys := sortedKeys(m.Tags)
		for _, k := range keys {
			w.WriteByte(',')
			w.WriteString(lpEscapeKey(k))
			w.WriteByte('=')
			w.WriteString(lpEscapeKey(m.Tags[k]))
		}
	}
	w.WriteByte(' ')
	fkeys := sortedFieldKeys(m.Fields)
	for i, k := range fkeys {
		if i > 0 {
			w.WriteByte(',')
		}
		w.WriteString(lpEscapeKey(k))
		w.WriteByte('=')
		writeFieldValue(w, m.Fields[k])
	}
	w.WriteByte(' ')
	ts := m.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	fmt.Fprintf(w, "%d\n", ts.UnixNano())
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
func sortedFieldKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// Local sort so the file doesn't depend on "sort" just for this.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// lpEscapeKey escapes characters that have line-protocol meaning in
// measurement / tag / field keys.
func lpEscapeKey(s string) string {
	r := strings.NewReplacer(",", `\,`, " ", `\ `, "=", `\=`)
	return r.Replace(s)
}

func writeFieldValue(w *bytes.Buffer, v any) {
	switch x := v.(type) {
	case float64:
		fmt.Fprintf(w, "%v", x)
	case float32:
		fmt.Fprintf(w, "%v", float64(x))
	case int:
		fmt.Fprintf(w, "%di", x)
	case int64:
		fmt.Fprintf(w, "%di", x)
	case uint64:
		fmt.Fprintf(w, "%di", int64(x))
	case bool:
		if x {
			w.WriteString("true")
		} else {
			w.WriteString("false")
		}
	case string:
		w.WriteByte('"')
		w.WriteString(strings.ReplaceAll(x, `"`, `\"`))
		w.WriteByte('"')
	default:
		fmt.Fprintf(w, "%v", v)
	}
}

// escape is a tiny URL query-param escaper — influx accepts the same
// rules as browsers; using net/url pulls in more than we need for
// two param values.
func escape(s string) string {
	r := strings.NewReplacer(" ", "%20", "&", "%26", "=", "%3D")
	return r.Replace(s)
}

// ── HTTP handlers ──────────────────────────────────────────────────

const (
	AuditMetricsUpdate = "metrics.update"
	AuditMetricsTest   = "metrics.test"
)

func handleMetricsSettings(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			cfg := store.metricsConfigSnapshot()
			if cfg == nil {
				cfg = &models.MetricsConfig{}
			}
			// Redact token on read.
			if cfg.Token != "" {
				cfg.Token = "********"
			}
			writeJSON(w, http.StatusOK, cfg)
		case http.MethodPut:
			var req models.MetricsConfig
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
				return
			}
			// Preserve token when operator sent back the mask.
			existing := store.metricsConfigSnapshot()
			if req.Token == "********" && existing != nil {
				req.Token = existing.Token
			}
			store.SetMetricsConfig(&req)
			audit(store, r, AuditMetricsUpdate, "metrics", map[string]string{
				"enabled": fmt.Sprintf("%v", req.Enabled),
				"bucket":  req.Bucket,
			}, true, "")
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			writeError(w, http.StatusMethodNotAllowed, "GET / PUT only")
		}
	}
}

// handleMetricsTest pushes a single test sample synchronously so the
// operator sees the actual Influx response immediately after saving.
func handleMetricsTest(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		cfg := store.metricsConfigSnapshot()
		if cfg == nil || !cfg.Enabled {
			writeError(w, http.StatusBadRequest, "metrics disabled")
			return
		}
		sample := metricSample{
			Measurement: strings.TrimSuffix(cfgPrefix(cfg), "") + "test",
			Tags:        map[string]string{"source": "kvm-center"},
			Fields:      map[string]any{"ok": int64(1)},
			Timestamp:   time.Now(),
		}
		if err := pushToInflux(cfg, []metricSample{sample}); err != nil {
			store.recordMetricsResult(err, 1)
			audit(store, r, AuditMetricsTest, "metrics", nil, false, err.Error())
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		store.recordMetricsResult(nil, 1)
		audit(store, r, AuditMetricsTest, "metrics", nil, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
	}
}

func cfgPrefix(cfg *models.MetricsConfig) string {
	if cfg.MeasurementPrefix != "" {
		return cfg.MeasurementPrefix
	}
	return metricsDefaultPrefix
}

// Ensure the sync import isn't eliminated — the worker uses a
// channel the field-level lock protects at Store level.
var _ = sync.Mutex{}
