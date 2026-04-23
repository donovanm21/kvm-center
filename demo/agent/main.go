// Command demo-agent impersonates every synthetic kvm-center
// hypervisor in the demo deployment from a single container.
// Reads the full fleet (hosts + their VMs + storage pools) from
// Mongo on startup, then spins one goroutine per host that POSTs
// telemetry to the server every 15 s and serves the agent HTTP API
// on a per-host port (8100 + N).
//
// Stage 1 (v5.33.7+): static metrics — numbers seeded by demo-seed
// are echoed back on every tick without drift. Stage 2 will add
// realistic drift + reactive /command state flipping; stage 3 will
// add scripted scenarios.
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
	"log"
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
	// Build one hostAgent per host.
	agents := make([]*hostAgent, 0, len(hostDocs))
	for _, d := range hostDocs {
		h := decodeHost(d)
		agents = append(agents, &hostAgent{
			host:    h,
			vms:     vmsByHost[h.ID],
			storage: storageByHost[h.ID],
		})
	}
	return agents, nil
}

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
	mux.HandleFunc("/command", handleCommand)
	mux.HandleFunc("/migrate-status", handleMigrateStatus)
	mux.HandleFunc("/cancel-migrate", handleCancel)
	mux.HandleFunc("/refresh-telemetry", func(w http.ResponseWriter, _ *http.Request) {
		go a.sendTelemetry()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"refreshed":true}`))
	})
	mux.HandleFunc("/snapshots/", handleSnapshots)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	addr := fmt.Sprintf(":%d", a.host.AgentPort)
	log.Printf("demo-agent: %s command server on %s", a.host.ID, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("demo-agent: %s listen error: %v", a.host.ID, err)
	}
}

func handleCommand(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "demo mode: command accepted (no real execution)",
	})
}

func handleMigrateStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"active": false, "phase": "idle", "progress": 0,
	})
}

func handleCancel(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

func handleSnapshots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	name := strings.TrimPrefix(r.URL.Path, "/snapshots/")
	// Deterministic number of fake snapshots from the VM name.
	var snaps []map[string]any
	count := len(name) % 4
	for i := 0; i < count; i++ {
		snaps = append(snaps, map[string]any{
			"name":          fmt.Sprintf("policy-pol_demo-2026042%d-03000%d", 1+i, i),
			"description":   "",
			"parent":        "",
			"state":         "running",
			"creation_time": time.Now().Add(-time.Duration(i+1) * time.Hour).Format(time.RFC3339),
		})
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
