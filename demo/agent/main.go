// Command demo-agent impersonates every synthetic kvm-center
// hypervisor in the demo deployment from a single container.
// Reads the full fleet (hosts + their VMs + storage pools) from
// Mongo on startup, then spins one goroutine per host that POSTs
// telemetry to the server every 15 s and serves the agent HTTP API
// on a per-host port (8100 + N).
//
// Stage 2 (current): per-metric drift keeps CPU / memory / disk
// numbers moving within believable bounds, and /command handlers
// mutate the in-memory VM state so clicking Start / Stop / Snapshot
// in the UI actually flips status / bumps snapshot count on the next
// telemetry tick.
//
// Stage 3 (planned) will add scripted scenarios on a loop
// (load spike, migration, host offline blip, notifications).
//
// One container, all 30 hosts, keeps the compose file trivial.
//
// Env:
//
//	KVM_CENTER_DEMO_MONGO_URL   Mongo URI (REQUIRED)
//	KVM_CENTER_DEMO_MONGO_DB    db name (default: kvm-center-demo)
//	KVM_CENTER_SERVER           control plane URL (REQUIRED)
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	mrand "math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"kvm-center/internal/models"
)

var serverURL string

// hostAgent owns one synthetic host's state + HTTP server. One goroutine
// per instance, all sharing the shared HTTP out-client.
type hostAgent struct {
	mu      sync.RWMutex
	host    *models.Host
	vms     []*models.VM
	storage []*models.StoragePool

	// baseline is captured once at boot — mean values we drift
	// AROUND so the UI never sees CPU walk off to 100% or 0%.
	baseline hostBaseline

	// rng is per-host so concurrent goroutines don't race on a
	// shared math/rand source.
	rng *mrand.Rand
}

// hostBaseline stores the seed's initial numbers as the "centre of
// gravity" for drift. On each tick we nudge current values a few %
// toward the baseline, then add a small random delta.
type hostBaseline struct {
	hostCPU, hostMem float64
	vmCPU            map[string]float64 // key: vm.UUID
	vmMem            map[string]uint64
}

func main() {
	mongoURI := mustEnv("KVM_CENTER_DEMO_MONGO_URL")
	mongoDB := envOr("KVM_CENTER_DEMO_MONGO_DB", "kvm-center-demo")
	serverURL = mustEnv("KVM_CENTER_SERVER")

	log.Printf("demo-agent: server=%s", serverURL)

	agents, err := loadFleet(mongoURI, mongoDB)
	if err != nil {
		log.Fatalf("load fleet: %v", err)
	}
	log.Printf("demo-agent: loaded %d synthetic hypervisors", len(agents))

	// One HTTP server per host, listening on a unique port. The
	// server's command dispatch reaches each via its host.AgentPort.
	for _, a := range agents {
		go startCommandServer(a)
	}

	// Telemetry loop — every agent fires every 15 s.
	for _, a := range agents {
		go a.telemetryLoop()
	}

	// Scripted scenarios on a loop: load spikes, offline blips,
	// snapshot policy simulator. See scenarios.go.
	startScenarios(agents)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("demo-agent: shutting down")
}

// ─── Fleet load ────────────────────────────────────────────────

func loadFleet(uri, dbName string) ([]*hostAgent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	defer cli.Disconnect(ctx)
	db := cli.Database(dbName)

	// Wait for seed to finish (any host exists).
	deadline := time.Now().Add(2 * time.Minute)
	for {
		n, err := db.Collection("hosts").CountDocuments(ctx, bson.D{})
		if err == nil && n > 0 {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("no hosts seeded after 2 min")
		}
		time.Sleep(2 * time.Second)
	}

	// Pull every host, VM, pool up front into in-memory maps.
	hostCur, err := db.Collection("hosts").Find(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("hosts find: %w", err)
	}
	var hostDocs []bson.M
	if err := hostCur.All(ctx, &hostDocs); err != nil {
		return nil, fmt.Errorf("hosts decode: %w", err)
	}

	vmCur, err := db.Collection("vms").Find(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("vms find: %w", err)
	}
	var vmDocs []bson.M
	if err := vmCur.All(ctx, &vmDocs); err != nil {
		return nil, fmt.Errorf("vms decode: %w", err)
	}

	stCur, err := db.Collection("storage").Find(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("storage find: %w", err)
	}
	var stDocs []bson.M
	if err := stCur.All(ctx, &stDocs); err != nil {
		return nil, fmt.Errorf("storage decode: %w", err)
	}

	// Group VMs + storage by host_id.
	vmsByHost := map[string][]*models.VM{}
	for _, d := range vmDocs {
		v := decodeVM(d)
		vmsByHost[v.HostID] = append(vmsByHost[v.HostID], v)
	}
	storageByHost := map[string][]*models.StoragePool{}
	for _, d := range stDocs {
		p := decodeStorage(d)
		storageByHost[p.HostID] = append(storageByHost[p.HostID], p)
	}
	// Build one hostAgent per host + snapshot baseline.
	agents := make([]*hostAgent, 0, len(hostDocs))
	for i, d := range hostDocs {
		h := decodeHost(d)
		vms := vmsByHost[h.ID]
		base := hostBaseline{
			hostCPU: h.CPUUsage, hostMem: h.MemUsage,
			vmCPU: make(map[string]float64, len(vms)),
			vmMem: make(map[string]uint64, len(vms)),
		}
		for _, v := range vms {
			base.vmCPU[v.UUID] = v.CPUUsage
			base.vmMem[v.UUID] = v.MemUsageMB
		}
		agents = append(agents, &hostAgent{
			host:     h,
			vms:      vms,
			storage:  storageByHost[h.ID],
			baseline: base,
			// Stagger host-RNG seeds so the fleet doesn't drift in
			// lock-step. Seed from boot-time + host index.
			rng: mrand.New(mrand.NewSource(time.Now().UnixNano() + int64(i))),
		})
	}
	return agents, nil
}

// drift nudges the host + per-VM metrics a small random step each
// tick. Values lerp back toward their baseline so nothing drifts
// permanently out of the seeded profile. Called from telemetryLoop
// before sendTelemetry.
func (a *hostAgent) drift() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.host == nil {
		return
	}
	// Host CPU + memory: 5% pull to baseline, ±3% noise.
	a.host.CPUUsage = clamp(a.host.CPUUsage+(a.baseline.hostCPU-a.host.CPUUsage)*0.05+(a.rng.Float64()-0.5)*6.0, 0.5, 99.0)
	a.host.MemUsage = clamp(a.host.MemUsage+(a.baseline.hostMem-a.host.MemUsage)*0.05+(a.rng.Float64()-0.5)*3.0, 5.0, 97.0)
	a.host.CPUUsage = round1(a.host.CPUUsage)
	a.host.MemUsage = round1(a.host.MemUsage)
	a.host.MemFreeMB = uint64(float64(a.host.MemTotalMB) * (1.0 - a.host.MemUsage/100.0))
	a.host.LastSeen = time.Now().UTC()

	// Per-VM drift, only for running VMs.
	for _, v := range a.vms {
		if v == nil || v.State != models.VMRunning {
			continue
		}
		base := a.baseline.vmCPU[v.UUID]
		v.CPUUsage = round1(clamp(v.CPUUsage+(base-v.CPUUsage)*0.07+(a.rng.Float64()-0.5)*12.0, 0.2, 98.0))
		baseMem := float64(a.baseline.vmMem[v.UUID])
		cur := float64(v.MemUsageMB)
		newMem := cur + (baseMem-cur)*0.08 + (a.rng.Float64()-0.5)*float64(v.MemTotalMB)*0.03
		// Keep memory usage within 10%..95% of total.
		cap95 := float64(v.MemTotalMB) * 0.95
		cap10 := float64(v.MemTotalMB) * 0.10
		if newMem < cap10 {
			newMem = cap10
		} else if newMem > cap95 {
			newMem = cap95
		}
		v.MemUsageMB = uint64(newMem)
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round1(f float64) float64 { return float64(int(f*10)) / 10 }

// decode* helpers normalise the driver's default lowercased keys
// back to snake_case so the models' JSON tags can rehydrate.
func decodeHost(m bson.M) *models.Host {
	var h models.Host
	remarshal(m, &h)
	return &h
}
func decodeVM(m bson.M) *models.VM {
	var v models.VM
	remarshal(m, &v)
	return &v
}
func decodeStorage(m bson.M) *models.StoragePool {
	var p models.StoragePool
	remarshal(m, &p)
	return &p
}

// remarshal converts a bson.M's lowercased keys to the JSON
// snake_case the models expect, then round-trips via JSON.
func remarshal(m bson.M, out any) {
	normalised := make(map[string]any, len(m))
	for k, v := range m {
		if k == "_id" {
			continue
		}
		normalised[keyMap[k]] = v
	}
	data, err := json.Marshal(normalised)
	if err != nil {
		log.Printf("demo-agent: remarshal encode: %v", err)
		return
	}
	if err := json.Unmarshal(data, out); err != nil {
		log.Printf("demo-agent: remarshal decode: %v", err)
	}
}

var keyMap = map[string]string{
	"id":                     "id",
	"ip":                     "ip",
	"agentport":              "agent_port",
	"cpuusage":               "cpu_usage",
	"cpucount":               "cpu_count",
	"memusage":               "mem_usage",
	"memtotalmb":             "mem_total_mb",
	"memfreemb":              "mem_free_mb",
	"vmcount":                "vm_count",
	"status":                 "status",
	"lastseen":               "last_seen",
	"os":                     "os",
	"kernelver":              "kernel_ver",
	"libvirtver":             "libvirt_ver",
	"uptime":                 "uptime",
	"agentversion":           "agent_version",
	"cpumodel":               "cpu_model",
	"sockets":                "sockets",
	"corespersocket":         "cores_per_socket",
	"threadspercore":         "threads_per_core",
	"numanodes":              "numa_nodes",
	"gpus":                   "gpus",
	"name":                   "name",
	"uuid":                   "uuid",
	"hostid":                 "host_id",
	"state":                  "state",
	"disktotalgb":            "disk_total_gb",
	"diskusagegb":            "disk_usage_gb",
	"diskpath":               "disk_path",
	"disktype":               "disk_type",
	"cpumode":                "cpu_mode",
	"autostart":              "autostart",
	"bootdev":                "boot_dev",
	"firmware":               "firmware",
	"machinetype":            "machine_type",
	"videomodel":             "video_model",
	"snapshotcount":          "snapshot_count",
	"networks":               "networks",
	"kvmtoolsversion":        "kvm_tools_version",
	"guestos":                "guest_os",
	"guestkernel":            "guest_kernel",
	"guesthostname":          "guest_hostname",
	"memusagemb":             "mem_usage_mb",
	"alldisks":               "all_disks",
	"guestagent":             "guest_agent",
	"guestsource":            "guest_source",
	"snapshotsblockedreason": "snapshots_blocked_reason",
	"path":                   "path",
	"type":                   "type",
	"pooltype":               "pool_type",
	"capacitygb":             "capacity_gb",
	"allocationgb":           "allocation_gb",
	"availablegb":            "available_gb",
	"persistent":             "persistent",
}

// ─── Telemetry ─────────────────────────────────────────────────

func (a *hostAgent) telemetryLoop() {
	// Stagger initial POSTs so 30 agents don't stampede the server
	// at boot. Deterministic stagger based on port number.
	time.Sleep(time.Duration(a.host.AgentPort%30) * 500 * time.Millisecond)
	a.sendTelemetry()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for range t.C {
		a.drift()
		a.sendTelemetry()
	}
}

var telClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
}

func (a *hostAgent) sendTelemetry() {
	a.mu.RLock()
	h := a.host
	vms := a.vms
	storage := a.storage
	a.mu.RUnlock()
	if h == nil {
		return
	}
	// Offline-blip scenario: the scheduler set a paused flag; stop
	// posting so the server's offline threshold kicks in.
	if fleet != nil && fleet.isPaused(h.ID) {
		return
	}
	payload := models.TelemetryPayload{
		HostID:         h.ID,
		IP:             h.IP,
		AgentPort:      h.AgentPort,
		OS:             h.OS,
		KernelVer:      h.KernelVer,
		LibvirtVer:     h.LibvirtVer,
		CPUUsage:       h.CPUUsage,
		CPUCount:       h.CPUCount,
		MemUsage:       h.MemUsage,
		MemTotalMB:     h.MemTotalMB,
		MemFreeMB:      h.MemFreeMB,
		Uptime:         h.Uptime,
		AgentVersion:   h.AgentVersion,
		CPUModel:       h.CPUModel,
		Sockets:        h.Sockets,
		CoresPerSocket: h.CoresPerSocket,
		ThreadsPerCore: h.ThreadsPerCore,
		NUMANodes:      h.NUMANodes,
		GPUs:           h.GPUs,
		Timestamp:      time.Now(),
	}
	for _, v := range vms {
		if v != nil {
			payload.VMs = append(payload.VMs, *v)
		}
	}
	for _, p := range storage {
		if p != nil {
			payload.Storage = append(payload.Storage, *p)
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("demo-agent: marshal %s: %v", h.ID, err)
		return
	}
	resp, err := telClient.Post(serverURL+"/api/telemetry", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("demo-agent: post %s: %v", h.ID, err)
		return
	}
	_ = resp.Body.Close()
}

// ─── Per-host command server ──────────────────────────────────

func startCommandServer(a *hostAgent) {
	mux := http.NewServeMux()
	mux.HandleFunc("/command", a.handleCommand)
	mux.HandleFunc("/migrate-status", a.handleMigrateStatus)
	mux.HandleFunc("/cancel-migrate", handleCancel)
	mux.HandleFunc("/refresh-telemetry", func(w http.ResponseWriter, _ *http.Request) {
		go a.sendTelemetry()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"refreshed":true}`))
	})
	mux.HandleFunc("/snapshots/", a.handleSnapshots)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	addr := fmt.Sprintf(":%d", a.host.AgentPort)
	log.Printf("demo-agent: %s command server on %s", a.host.ID, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("demo-agent: %s listen error: %v", a.host.ID, err)
	}
}

// demoCommandRequest matches the server's CommandRequest shape —
// inlined so we don't drag the server package in.
type demoCommandRequest struct {
	RequestID string            `json:"request_id"`
	Action    string            `json:"action"`
	VMName    string            `json:"vm_name"`
	VMUUID    string            `json:"vm_uuid"`
	Params    map[string]string `json:"params"`
}

// handleCommand accepts the lifecycle actions the server forwards
// and mutates the in-memory VM state so the next telemetry tick
// reflects the change in the UI. Fully synthetic — no virsh
// involved. Stage 2 (v5.33.7+). Supported actions:
//
//	START / POWER_CYCLE       → state = running
//	STOP / FORCESTOP          → state = stopped
//	RESTART                   → stays running (state unchanged)
//	SNAPSHOT                  → snapshot_count++
//	DELETE_SNAPSHOT           → snapshot_count-- (floor 0)
//	REVERT_SNAPSHOT           → state = running + message
//	EDIT_VM                   → applies cpu_count / mem_total_mb params
//	DELETE_VM                 → removes VM from the host's list
//	CHANGE_MEDIA              → accepts, no-op (no UI effect)
//	MIGRATE / OFFLINE_MIGRATE → refuse in demo stage 2 (too stateful
//	                            to fake convincingly — stage 3 will
//	                            add scripted live migrations)
func (a *hostAgent) handleCommand(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req demoCommandRequest
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)
	action := strings.ToUpper(req.Action)

	resp := map[string]any{
		"request_id": req.RequestID,
		"success":    true,
		"message":    "demo: " + action + " applied",
	}

	a.mu.Lock()
	vm := a.findVMLocked(req.VMUUID, req.VMName)
	switch action {
	case "START", "POWER_CYCLE":
		if vm != nil {
			vm.State = models.VMRunning
			if base, ok := a.baseline.vmCPU[vm.UUID]; ok {
				vm.CPUUsage = base
			}
		}
	case "STOP", "FORCESTOP":
		if vm != nil {
			vm.State = models.VMStopped
			vm.CPUUsage = 0
			vm.MemUsageMB = 0
		}
	case "RESTART":
		// RESTART in the real agent does a guest reboot; state stays
		// "running" on success. Reset usage to baseline.
		if vm != nil {
			if base, ok := a.baseline.vmCPU[vm.UUID]; ok {
				vm.CPUUsage = base
			}
		}
	case "SNAPSHOT":
		if vm != nil {
			vm.SnapshotCount++
		}
	case "DELETE_SNAPSHOT":
		if vm != nil && vm.SnapshotCount > 0 {
			vm.SnapshotCount--
		}
	case "REVERT_SNAPSHOT":
		if vm != nil {
			vm.State = models.VMRunning
		}
		resp["message"] = "demo: snapshot reverted"
	case "EDIT_VM":
		updated := map[string]any{}
		if vm != nil {
			if v := req.Params["vcpus"]; v != "" {
				if n := atoi(v); n > 0 {
					vm.CPUCount = n
					updated["cpu_count"] = n
				}
			}
			if v := req.Params["mem_mb"]; v != "" {
				if n := atoi(v); n > 0 {
					vm.MemTotalMB = uint64(n)
					updated["mem_total_mb"] = uint64(n)
				}
			}
		}
		if len(updated) > 0 {
			resp["updated_fields"] = updated
		}
	case "DELETE_VM":
		if vm != nil {
			filtered := a.vms[:0]
			for _, v := range a.vms {
				if v.UUID != vm.UUID {
					filtered = append(filtered, v)
				}
			}
			a.vms = filtered
		}
	case "CHANGE_MEDIA":
		resp["message"] = "demo: media change accepted (UI-only in demo)"
	case "MIGRATE", "OFFLINE_MIGRATE":
		// Stage 3: accept the migration + let /migrate-status render
		// a ~3-min progress arc. The scenarios module holds the
		// synthMigration + handles the eventual host-flip when the
		// arc completes.
		dst := req.Params["destination"]
		if dst == "" {
			dst = req.Params["dest_host"]
		}
		// Resolve dst — server sends an IP (demo-agent-NN) so map
		// it back to a host ID using the fleet registry.
		if dst != "" {
			if dstAgent := resolveDstByAlias(dst); dstAgent != nil {
				dst = dstAgent.host.ID
			}
		}
		if dst == "" || vm == nil || dst == a.host.ID {
			resp["success"] = false
			resp["message"] = "demo: migration needs a valid destination different from source"
			a.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		// Unlock before calling startMigration so it can take fleet
		// locks in the right order.
		vmName := vm.Name
		vmUUID := vm.UUID
		a.mu.Unlock()
		m := startMigration(a.host.ID, vmUUID, vmName, dst)
		log.Printf("demo: migration started %s %s → %s (duration=%s)", vmName, a.host.ID, dst, m.duration)
		resp["message"] = "demo: migration started (will take " + m.duration.Truncate(time.Second).String() + ")"
		_ = json.NewEncoder(w).Encode(resp)
		return
	default:
		resp["message"] = "demo: unknown action: " + action
	}
	a.mu.Unlock()

	// Kick an immediate telemetry POST so the SPA reflects the
	// change without waiting for the 15s tick. Keeps the demo
	// feeling snappy when the operator clicks Start/Stop.
	go a.sendTelemetry()

	_ = json.NewEncoder(w).Encode(resp)
}

// findVMLocked looks up a VM by UUID first, then by name. Caller
// must hold a.mu.
func (a *hostAgent) findVMLocked(uuid, name string) *models.VM {
	for _, v := range a.vms {
		if v == nil {
			continue
		}
		if uuid != "" && strings.EqualFold(v.UUID, uuid) {
			return v
		}
		if name != "" && v.Name == name {
			return v
		}
	}
	return nil
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// handleMigrateStatus looks up any in-flight synthetic migration
// for this host and reports a progress arc. The server's
// pollMigrationStatus goroutine drives the migration record off
// these replies, so a 0→100 arc here shows up as a moving
// progress bar in the SPA + eventually flips the record to
// complete.
func (a *hostAgent) handleMigrateStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	m := migrationFor(a.host.ID)
	if m == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": false, "phase": "idle", "progress": 0,
		})
		return
	}
	now := time.Now()
	p := m.progress(now)
	if p >= 100 {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": false, "phase": "complete", "progress": 100,
			"vm_name": m.vmName, "detail": "Migration finished",
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"active": true, "phase": m.phase(now), "progress": p,
		"vm_name": m.vmName, "detail": fmt.Sprintf("Copying memory + disk (%d%%)", p),
	})
}

// resolveDstByAlias maps a destination value (could be a docker
// alias like "demo-agent-04" sent by the server as host.IP, or the
// host ID itself) to its fleet agent.
func resolveDstByAlias(dst string) *hostAgent {
	fleet.mu.RLock()
	defer fleet.mu.RUnlock()
	for _, a := range fleet.agents {
		if a.host.IP == dst || a.host.ID == dst {
			return a
		}
	}
	return nil
}

func handleCancel(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

// handleSnapshots returns a synthetic per-VM snapshot listing. Count
// reflects the VM's SnapshotCount so Snapshot creates in the UI
// actually produce visible entries here.
func (a *hostAgent) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	name := strings.TrimPrefix(r.URL.Path, "/snapshots/")
	a.mu.RLock()
	var snapCount int
	for _, v := range a.vms {
		if v != nil && v.Name == name {
			snapCount = v.SnapshotCount
			break
		}
	}
	a.mu.RUnlock()
	var snaps []map[string]any
	for i := 0; i < snapCount; i++ {
		ts := time.Now().Add(-time.Duration(i+1) * time.Hour)
		snaps = append(snaps, map[string]any{
			"name":          fmt.Sprintf("snap-%d", ts.Unix()),
			"description":   "",
			"parent":        "",
			"state":         "running",
			"creation_time": ts.Format(time.RFC3339),
		})
	}
	if snaps == nil {
		snaps = []map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(snaps)
}

// ─── Helpers ───────────────────────────────────────────────────

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("demo-agent: %s is required", k)
	}
	return v
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
