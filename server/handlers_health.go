package main

// System health endpoint (v5.33.0, P1.1). One URL that answers
// "is the deployment OK?" at a glance — admin-only, cached for 15s,
// polled by the SPA health page every 10-15s.
//
// Design principles:
//   - Every subsystem returns a { status, detail, measured_at } triple
//     so the SPA can render a uniform card without special-casing.
//   - "status" is green / amber / red. Amber = degraded but operable;
//     red = needs attention right now.
//   - No probe should trip the thing it's measuring. Cache the result
//     for healthCacheTTL so a spike of concurrent page loads doesn't
//     stampede libvirt/Mongo.
//   - Cheap reads only. Heavy work (iterating every hypervisor cert,
//     pinging every agent) happens on the cache refresh path, not the
//     request hot path.
//   - Rollup = worst-of across subsystems. Red if any red; amber if any
//     amber; otherwise green.

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"time"

	"kvm-center/internal/models"
)

const (
	healthStatusGreen = "green"
	healthStatusAmber = "amber"
	healthStatusRed   = "red"

	healthCacheTTL = 15 * time.Second
)

// SystemHealth is the whole response shape. Each named subsystem
// returns a {status, headline, detail} triple; `Overall` is the
// rollup. `Aggregates` provides fleet-wide numbers + time-series
// windows the UI can render as gauges + line charts without a
// follow-up round trip. `HostBreakdown` + `Problems` feed the
// table + problems list at the bottom of the dashboard.
type SystemHealth struct {
	Overall       string            `json:"overall"` // green | amber | red
	GeneratedAt   time.Time         `json:"generated_at"`
	ControlPlane  SubsystemHealth   `json:"control_plane"`
	Database      SubsystemHealth   `json:"database"`
	TLS           SubsystemHealth   `json:"tls"`
	Hypervisors   SubsystemHealth   `json:"hypervisors"`
	GuestTools    SubsystemHealth   `json:"guest_tools"`
	Migrations    SubsystemHealth   `json:"migrations"`
	Snapshots     SubsystemHealth   `json:"snapshots"`
	CommandLog    SubsystemHealth   `json:"command_log"`
	Aggregates    HealthAggregates  `json:"aggregates"`
	HostBreakdown []HostHealthBrief `json:"host_breakdown"`
	Problems      []HealthProblem   `json:"problems"`
}

// HealthAggregates carries fleet-wide numbers + recent time-series so
// the UI can render gauges + 1h line charts without follow-up calls.
type HealthAggregates struct {
	FleetCPUPct     float64         `json:"fleet_cpu_pct"`      // current weighted avg
	FleetMemPct     float64         `json:"fleet_mem_pct"`      // current weighted avg
	FleetVMCount    int             `json:"fleet_vm_count"`     // total VMs across hosts
	FleetVMRunning  int             `json:"fleet_vm_running"`   // running only
	FleetCPUCores   int             `json:"fleet_cpu_cores"`    // sum of host CPUCount
	FleetMemTotalGB float64         `json:"fleet_mem_total_gb"` // sum of host MemTotalMB
	MigrationsToday int             `json:"migrations_today"`   // count of records started in last 24h
	SnapshotBlocked int             `json:"snapshot_blocked"`   // VMs with SnapshotsBlockedReason set
	Series          AggregateSeries `json:"series"`
}

// AggregateSeries is a thin DTO of parallel arrays — easy to iterate
// in the SPA, and trivially JSON-serialisable. Timestamps in unix
// seconds UTC. Sampled from the per-host metric ring buffers; each
// tick is the fleet-weighted average at that instant.
type AggregateSeries struct {
	T      []int64   `json:"t"`       // unix seconds
	CPUPct []float64 `json:"cpu_pct"` // 0–100
	MemPct []float64 `json:"mem_pct"` // 0–100
	VMs    []int     `json:"vms"`     // running VM count across fleet
}

// HostHealthBrief is one row in the host breakdown list. Keep fields
// narrow so the whole response stays small even on a large fleet.
type HostHealthBrief struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"` // online | offline
	AgentVersion string  `json:"agent_version,omitempty"`
	CPUPct       float64 `json:"cpu_pct"`
	MemPct       float64 `json:"mem_pct"`
	VMCount      int     `json:"vm_count"`
	StoragePct   float64 `json:"storage_pct"`
	LastSeen     int64   `json:"last_seen"` // unix seconds
	Outdated     bool    `json:"outdated,omitempty"`
	Maintenance  bool    `json:"maintenance,omitempty"`
}

// HealthProblem is one aggregated issue surfaced on the dashboard —
// offline hosts, stranded commands, soon-to-expire certs, stuck
// migrations. Kept separate from audit events: problems are the
// **current** state, not history.
type HealthProblem struct {
	Severity string `json:"severity"` // amber | red (green items aren't problems)
	Source   string `json:"source"`   // "hypervisors" | "tls" | "migrations" | "command_log" | "snapshots"
	Resource string `json:"resource"`
	Summary  string `json:"summary"`
}

type SubsystemHealth struct {
	Status     string            `json:"status"` // green | amber | red
	Headline   string            `json:"headline"`
	Detail     map[string]string `json:"detail,omitempty"`
	MeasuredAt time.Time         `json:"measured_at"`
}

// healthCache holds the last computed SystemHealth so concurrent
// operator page loads share one snapshot. Refreshed lazily in a
// singleflight fashion: the first request past healthCacheTTL does
// the work, others wait on cacheMu.
var healthCache struct {
	mu         sync.Mutex
	value      *SystemHealth
	computedAt time.Time
}

func handleSystemHealth(store *Store, ca *ClusterCA, reloader *CertReloader, signer *GrantSigner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		h := getCachedSystemHealth(store, ca, reloader, signer)
		writeJSON(w, http.StatusOK, h)
	}
}

func getCachedSystemHealth(store *Store, ca *ClusterCA, reloader *CertReloader, signer *GrantSigner) *SystemHealth {
	healthCache.mu.Lock()
	defer healthCache.mu.Unlock()
	if healthCache.value != nil && time.Since(healthCache.computedAt) < healthCacheTTL {
		return healthCache.value
	}
	h := computeSystemHealth(store, ca, reloader, signer)
	healthCache.value = h
	healthCache.computedAt = time.Now()
	return h
}

func computeSystemHealth(store *Store, ca *ClusterCA, reloader *CertReloader, signer *GrantSigner) *SystemHealth {
	now := time.Now().UTC()
	h := &SystemHealth{GeneratedAt: now}
	h.ControlPlane = checkControlPlane()
	h.Database = checkDatabase(store)
	h.TLS = checkTLS(ca, reloader, signer)
	h.Hypervisors = checkHypervisors(store)
	h.GuestTools = checkGuestTools(store)
	h.Migrations = checkMigrations(store)
	h.Snapshots = checkSnapshots(store)
	h.CommandLog = checkCommandLog(store)
	h.Overall = rollup(
		h.ControlPlane, h.Database, h.TLS, h.Hypervisors,
		h.GuestTools, h.Migrations, h.Snapshots, h.CommandLog,
	)
	h.Aggregates = computeAggregates(store)
	h.HostBreakdown = computeHostBreakdown(store)
	h.Problems = computeProblems(store, ca, reloader)
	return h
}

// computeAggregates sums fleet-wide capacity + builds a 1h aggregate
// time-series by averaging per-host ring samples at each timestamp.
// Approximation: we line up each host's samples chronologically and
// compute a weighted average using hostMetricCap slots. Not pixel-
// perfect if hosts don't share the same 30s tick phase, but close
// enough for a dashboard.
func computeAggregates(store *Store) HealthAggregates {
	var agg HealthAggregates
	hosts := store.GetHosts()
	var cpuWeighted, memWeighted, weight float64
	for _, h := range hosts {
		if h == nil {
			continue
		}
		agg.FleetCPUCores += h.CPUCount
		agg.FleetMemTotalGB += float64(h.MemTotalMB) / 1024
		if h.Status == models.HostOnline {
			w := float64(h.CPUCount)
			if w == 0 {
				w = 1
			}
			cpuWeighted += h.CPUUsage * w
			memWeighted += h.MemUsage * w
			weight += w
		}
	}
	if weight > 0 {
		agg.FleetCPUPct = cpuWeighted / weight
		agg.FleetMemPct = memWeighted / weight
	}
	// VM count + running count across fleet.
	for _, hostID := range listHostIDs(store) {
		for _, v := range store.GetVMs(hostID) {
			if v == nil {
				continue
			}
			agg.FleetVMCount++
			if v.State == models.VMRunning {
				agg.FleetVMRunning++
			}
		}
	}
	// Snapshot-blocked count.
	for _, hostID := range listHostIDs(store) {
		for _, v := range store.GetVMs(hostID) {
			if v != nil && v.SnapshotsBlockedReason != "" {
				agg.SnapshotBlocked++
			}
		}
	}
	// Migrations started in the last 24h.
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	for _, m := range store.GetMigrations() {
		if m != nil && m.StartedAt >= cutoff {
			agg.MigrationsToday++
		}
	}
	agg.Series = aggregateSeriesFromHosts(store)
	return agg
}

// aggregateSeriesFromHosts averages each host's metric ring at each
// slot index (hostMetricCap slots → 1h window at 30s tick). Uses the
// host's own timestamp for the first bucket so charts plot against
// real time rather than slot index.
func aggregateSeriesFromHosts(store *Store) AggregateSeries {
	hosts := store.GetHosts()
	if len(hosts) == 0 {
		return AggregateSeries{T: []int64{}, CPUPct: []float64{}, MemPct: []float64{}, VMs: []int{}}
	}
	// Collect every host's snapshot.
	var all [][]hostMetricSample
	for _, h := range hosts {
		if h == nil {
			continue
		}
		s := store.HostMetricsSnapshot(h.ID)
		if len(s) == 0 {
			continue
		}
		all = append(all, s)
	}
	if len(all) == 0 {
		return AggregateSeries{T: []int64{}, CPUPct: []float64{}, MemPct: []float64{}, VMs: []int{}}
	}
	// Align by taking the min length (so every host contributes a
	// valid reading at every emitted slot). Use the slowest host's
	// tick as the series time axis.
	n := len(all[0])
	for _, s := range all {
		if len(s) < n {
			n = len(s)
		}
	}
	t := make([]int64, n)
	cpu := make([]float64, n)
	mem := make([]float64, n)
	vms := make([]int, n)
	for i := 0; i < n; i++ {
		var cpuSum, memSum float64
		var vmSum int
		for _, s := range all {
			cpuSum += s[len(s)-n+i].CPU
			memSum += s[len(s)-n+i].Mem
			vmSum += s[len(s)-n+i].VMs
		}
		t[i] = all[0][len(all[0])-n+i].T.Unix()
		cpu[i] = cpuSum / float64(len(all))
		mem[i] = memSum / float64(len(all))
		vms[i] = vmSum
	}
	return AggregateSeries{T: t, CPUPct: cpu, MemPct: mem, VMs: vms}
}

func listHostIDs(store *Store) []string {
	hosts := store.GetHosts()
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if h != nil {
			out = append(out, h.ID)
		}
	}
	return out
}

// computeHostBreakdown is the per-row data for the dashboard's host
// table. Derived fields mirror the per-host card on the Hosts page
// so the operator has one consistent shape to read.
func computeHostBreakdown(store *Store) []HostHealthBrief {
	hosts := store.GetHosts()
	out := make([]HostHealthBrief, 0, len(hosts))
	for _, h := range hosts {
		if h == nil {
			continue
		}
		pools := store.GetStorage(h.ID)
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
		out = append(out, HostHealthBrief{
			ID:           h.ID,
			Status:       string(h.Status),
			AgentVersion: h.AgentVersion,
			CPUPct:       h.CPUUsage,
			MemPct:       h.MemUsage,
			VMCount:      h.VMCount,
			StoragePct:   storagePct,
			LastSeen:     h.LastSeen.Unix(),
			Outdated:     h.AgentVersion != "" && h.AgentVersion != Version,
			Maintenance:  h.MaintenanceMode,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// computeProblems aggregates the "needs attention right now" items
// into a flat list for the dashboard's bottom panel. Duplicates what
// the subsystem cards show but concentrated — the operator should be
// able to skim one table and triage.
func computeProblems(store *Store, ca *ClusterCA, reloader *CertReloader) []HealthProblem {
	probs := []HealthProblem{}
	nowT := time.Now()

	// Offline hypervisors.
	for _, h := range store.GetHosts() {
		if h == nil {
			continue
		}
		if h.Status != models.HostOnline {
			probs = append(probs, HealthProblem{
				Severity: "red",
				Source:   "hypervisors",
				Resource: "host/" + h.ID,
				Summary:  "Host offline since " + h.LastSeen.UTC().Format(time.RFC3339),
			})
		}
	}
	// Outdated hypervisor agents (amber).
	for _, h := range store.GetHosts() {
		if h == nil || h.AgentVersion == "" || h.AgentVersion == Version {
			continue
		}
		if h.Status != models.HostOnline {
			continue // already flagged as offline
		}
		probs = append(probs, HealthProblem{
			Severity: "amber",
			Source:   "hypervisors",
			Resource: "host/" + h.ID,
			Summary:  "Agent on " + h.AgentVersion + "; control plane is " + Version,
		})
	}
	// Expiring certs.
	if reloader != nil {
		if c, err := reloader.CurrentCert(); err == nil {
			days := daysUntil(c.NotAfter, nowT)
			if days < leafCriticalDays {
				probs = append(probs, HealthProblem{
					Severity: "red",
					Source:   "tls",
					Resource: "server-cert",
					Summary:  "Server TLS cert expires in " + itoa(days) + " days",
				})
			} else if days < leafWarnDays {
				probs = append(probs, HealthProblem{
					Severity: "amber",
					Source:   "tls",
					Resource: "server-cert",
					Summary:  "Server TLS cert expires in " + itoa(days) + " days",
				})
			}
		}
	}
	if ca != nil {
		if cert := ca.Cert(); cert != nil {
			days := daysUntil(cert.NotAfter, nowT)
			if days < caCriticalDays {
				probs = append(probs, HealthProblem{
					Severity: "red",
					Source:   "tls",
					Resource: "cluster-ca",
					Summary:  "Cluster CA expires in " + itoa(days) + " days",
				})
			} else if days < caWarnDays {
				probs = append(probs, HealthProblem{
					Severity: "amber",
					Source:   "tls",
					Resource: "cluster-ca",
					Summary:  "Cluster CA expires in " + itoa(days) + " days",
				})
			}
		}
	}
	// Stuck migrations (running > 90 min).
	for _, m := range store.GetMigrations() {
		if m == nil || m.Status != "running" {
			continue
		}
		age := nowT.Sub(time.Unix(m.StartedAt, 0))
		if age > 90*time.Minute {
			probs = append(probs, HealthProblem{
				Severity: "red",
				Source:   "migrations",
				Resource: "migration/" + m.ID,
				Summary:  "Running " + age.Round(time.Minute).String() + " — past the 90 min in-flight timeout",
			})
		}
	}
	// Stranded command log entries.
	if store.commandLog != nil {
		for _, e := range store.commandLog.List(0) {
			if e.Outcome == "unknown" {
				probs = append(probs, HealthProblem{
					Severity: "amber",
					Source:   "command_log",
					Resource: "request/" + e.RequestID,
					Summary:  e.Action + " on " + e.VMName + " stranded; awaiting operator resolution",
				})
			}
		}
	}
	// Snapshot-blocked VMs.
	for _, hostID := range listHostIDs(store) {
		for _, v := range store.GetVMs(hostID) {
			if v != nil && v.SnapshotsBlockedReason != "" {
				probs = append(probs, HealthProblem{
					Severity: "amber",
					Source:   "snapshots",
					Resource: "vm/" + v.UUID,
					Summary:  v.Name + ": snapshots blocked (" + v.SnapshotsBlockedReason + ")",
				})
			}
		}
	}
	return probs
}

func rollup(subs ...SubsystemHealth) string {
	worst := healthStatusGreen
	for _, s := range subs {
		if s.Status == healthStatusRed {
			return healthStatusRed
		}
		if s.Status == healthStatusAmber {
			worst = healthStatusAmber
		}
	}
	return worst
}

// checkControlPlane: version + uptime + goroutine count. Amber when
// the goroutine count spikes past a loose sanity cap (something's
// probably leaking) — not red, because a temporary spike during a
// migration storm is normal.
func checkControlPlane() SubsystemHealth {
	now := time.Now().UTC()
	uptime := time.Since(serverStartedAt).Round(time.Second)
	goroutines := runtime.NumGoroutine()
	detail := map[string]string{
		"version":    Version,
		"git_sha":    GitSHA,
		"go_version": runtime.Version(),
		"uptime":     uptime.String(),
		"goroutines": fmt.Sprintf("%d", goroutines),
	}
	status := healthStatusGreen
	headline := "Running " + Version + " for " + uptime.String()
	// 2000 is a wide cap — typical steady-state is under 200.
	if goroutines > 2000 {
		status = healthStatusAmber
		headline = fmt.Sprintf("Goroutine count high: %d", goroutines)
	}
	return SubsystemHealth{Status: status, Headline: headline, Detail: detail, MeasuredAt: now}
}

// checkDatabase: ping the backend via its Info(ctx) call, which
// already ships a Healthy flag + endpoint + size.
func checkDatabase(store *Store) SubsystemHealth {
	now := time.Now().UTC()
	if store.backend == nil {
		return SubsystemHealth{Status: healthStatusAmber, Headline: "No backend configured", MeasuredAt: now}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info := store.backend.Info(ctx)
	detail := map[string]string{
		"type":     info.Type,
		"endpoint": info.Endpoint,
	}
	if info.SizeBytes > 0 {
		detail["size_bytes"] = fmt.Sprintf("%d", info.SizeBytes)
	}
	if info.LastModified != "" {
		detail["last_modified"] = info.LastModified
	}
	if !info.Healthy {
		return SubsystemHealth{
			Status:     healthStatusRed,
			Headline:   "Backend unhealthy: " + info.Error,
			Detail:     detail,
			MeasuredAt: now,
		}
	}
	return SubsystemHealth{
		Status:     healthStatusGreen,
		Headline:   info.Type + " reachable",
		Detail:     detail,
		MeasuredAt: now,
	}
}

// checkTLS: server cert + cluster CA + grant signer. Reuses the
// expiry thresholds from cert_watcher.go.
func checkTLS(ca *ClusterCA, reloader *CertReloader, signer *GrantSigner) SubsystemHealth {
	now := time.Now().UTC()
	detail := map[string]string{}
	worst := healthStatusGreen
	headline := "All certificates healthy"
	nowT := time.Now()
	if reloader != nil {
		if c, err := reloader.CurrentCert(); err == nil {
			days := daysUntil(c.NotAfter, nowT)
			detail["server_cert_days"] = fmt.Sprintf("%d", days)
			detail["server_cert_not_after"] = c.NotAfter.UTC().Format(time.RFC3339)
			if days < leafCriticalDays {
				worst = healthStatusRed
				headline = fmt.Sprintf("Server cert expires in %d days", days)
			} else if days < leafWarnDays && worst == healthStatusGreen {
				worst = healthStatusAmber
				headline = fmt.Sprintf("Server cert expires in %d days", days)
			}
		}
	}
	if ca != nil {
		if cert := ca.Cert(); cert != nil {
			days := daysUntil(cert.NotAfter, nowT)
			detail["ca_days"] = fmt.Sprintf("%d", days)
			detail["ca_not_after"] = cert.NotAfter.UTC().Format(time.RFC3339)
			if days < caCriticalDays && worst != healthStatusRed {
				worst = healthStatusRed
				headline = fmt.Sprintf("Cluster CA expires in %d days", days)
			} else if days < caWarnDays && worst == healthStatusGreen {
				worst = healthStatusAmber
				headline = fmt.Sprintf("Cluster CA expires in %d days", days)
			}
		}
	}
	if signer != nil {
		_, prev, rotAt, grace := signer.RotationState()
		if prev != "" {
			detail["grant_signer_rotating"] = "true"
			detail["grant_rotated_at"] = rotAt.Format(time.RFC3339)
			detail["grant_grace_expires_at"] = grace.Format(time.RFC3339)
		}
	}
	return SubsystemHealth{Status: worst, Headline: headline, Detail: detail, MeasuredAt: now}
}

// checkHypervisors: count online / offline / version-drifted agents.
// Red if any hypervisor has gone offline within the normal
// telemetry window; amber if any agent is on a non-current version.
func checkHypervisors(store *Store) SubsystemHealth {
	now := time.Now().UTC()
	hosts := store.GetHosts()
	total, online, offline, outdated := 0, 0, 0, 0
	outdatedList := []string{}
	for _, h := range hosts {
		if h == nil {
			continue
		}
		total++
		if h.Status != models.HostOnline {
			offline++
		} else {
			online++
		}
		if h.AgentVersion != "" && h.AgentVersion != Version {
			outdated++
			outdatedList = append(outdatedList, h.ID+"="+h.AgentVersion)
		}
	}
	detail := map[string]string{
		"total":   fmt.Sprintf("%d", total),
		"online":  fmt.Sprintf("%d", online),
		"offline": fmt.Sprintf("%d", offline),
	}
	if len(outdatedList) > 0 {
		sort.Strings(outdatedList)
		detail["outdated_agents"] = fmt.Sprintf("%d", outdated)
		detail["outdated_list"] = fmt.Sprintf("%v", outdatedList)
	}
	if total == 0 {
		return SubsystemHealth{Status: healthStatusAmber, Headline: "No hypervisors enrolled", Detail: detail, MeasuredAt: now}
	}
	switch {
	case offline > 0:
		return SubsystemHealth{Status: healthStatusRed, Headline: fmt.Sprintf("%d of %d hypervisors offline", offline, total), Detail: detail, MeasuredAt: now}
	case outdated > 0:
		return SubsystemHealth{Status: healthStatusAmber, Headline: fmt.Sprintf("%d hypervisor agent(s) on outdated version", outdated), Detail: detail, MeasuredAt: now}
	default:
		return SubsystemHealth{Status: healthStatusGreen, Headline: fmt.Sprintf("%d of %d hypervisors online", online, total), Detail: detail, MeasuredAt: now}
	}
}

// checkGuestTools: count guests reporting vs expected, count
// version-drifted. Stale = no telemetry in >guestToolsFreshness
// (already enforced by the Mongo TTL).
func checkGuestTools(store *Store) SubsystemHealth {
	now := time.Now().UTC()
	store.mu.RLock()
	total := len(store.guestData)
	versions := map[string]int{}
	for _, g := range store.guestData {
		if g == nil {
			continue
		}
		versions[g.Payload.KVMToolsVersion]++
	}
	store.mu.RUnlock()

	outdated := 0
	for v, c := range versions {
		if v != Version && v != "" {
			outdated += c
		}
	}
	detail := map[string]string{
		"total":    fmt.Sprintf("%d", total),
		"outdated": fmt.Sprintf("%d", outdated),
	}
	for v, c := range versions {
		if v == "" {
			continue
		}
		detail["version_"+v] = fmt.Sprintf("%d", c)
	}
	status := healthStatusGreen
	headline := fmt.Sprintf("%d guests reporting", total)
	if outdated > 0 {
		status = healthStatusAmber
		headline = fmt.Sprintf("%d of %d guests on outdated kvm-tools", outdated, total)
	}
	return SubsystemHealth{Status: status, Headline: headline, Detail: detail, MeasuredAt: now}
}

// checkMigrations: active count + any records running longer than
// the 90-min in-flight timeout (the boot reconciler normally
// catches these, but during the boot-settle window they'll still
// be visible).
func checkMigrations(store *Store) SubsystemHealth {
	nowT := time.Now()
	now := nowT.UTC()
	active, stuck := 0, 0
	for _, m := range store.GetMigrations() {
		if m == nil || m.Status != "running" {
			continue
		}
		active++
		if nowT.Sub(time.Unix(m.StartedAt, 0)) > 90*time.Minute {
			stuck++
		}
	}
	detail := map[string]string{
		"active": fmt.Sprintf("%d", active),
		"stuck":  fmt.Sprintf("%d", stuck),
	}
	switch {
	case stuck > 0:
		return SubsystemHealth{Status: healthStatusRed, Headline: fmt.Sprintf("%d stuck migration(s) past timeout", stuck), Detail: detail, MeasuredAt: now}
	case active > 0:
		return SubsystemHealth{Status: healthStatusGreen, Headline: fmt.Sprintf("%d migration(s) in flight", active), Detail: detail, MeasuredAt: now}
	default:
		return SubsystemHealth{Status: healthStatusGreen, Headline: "No active migrations", Detail: detail, MeasuredAt: now}
	}
}

// checkSnapshots: count VMs blocked from taking snapshots + any
// recent policy-run failures.
func checkSnapshots(store *Store) SubsystemHealth {
	now := time.Now().UTC()
	blocked := 0
	for _, vms := range store.vms {
		for _, v := range vms {
			if v != nil && v.SnapshotsBlockedReason != "" {
				blocked++
			}
		}
	}
	detail := map[string]string{
		"snapshot_blocked_vms": fmt.Sprintf("%d", blocked),
	}
	status := healthStatusGreen
	headline := "All VMs snapshot-capable"
	if blocked > 0 {
		status = healthStatusAmber
		headline = fmt.Sprintf("%d VM(s) have snapshots blocked (UEFI pflash)", blocked)
	}
	return SubsystemHealth{Status: status, Headline: headline, Detail: detail, MeasuredAt: now}
}

// checkCommandLog: count entries in "unknown" outcome (stranded by
// a restart and not yet resolved by the operator).
func checkCommandLog(store *Store) SubsystemHealth {
	now := time.Now().UTC()
	if store.commandLog == nil {
		return SubsystemHealth{Status: healthStatusGreen, Headline: "Command log disabled", MeasuredAt: now}
	}
	unknown := 0
	for _, e := range store.commandLog.List(0) {
		if e.Outcome == "unknown" {
			unknown++
		}
	}
	detail := map[string]string{
		"unknown_outcome": fmt.Sprintf("%d", unknown),
	}
	status := healthStatusGreen
	headline := "No stranded commands"
	if unknown > 0 {
		status = healthStatusAmber
		headline = fmt.Sprintf("%d command(s) stranded as 'unknown' awaiting operator review", unknown)
	}
	return SubsystemHealth{Status: status, Headline: headline, Detail: detail, MeasuredAt: now}
}
