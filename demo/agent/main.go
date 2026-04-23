// Command demo-agent impersonates a kvm-center hypervisor agent for
// the demo deployment. Reads its host + VM fixtures directly from
// the demo Mongo backend on startup, then POSTs telemetry to the
// server every 15 s and serves the agent HTTP API on :8081 so the
// server can issue (no-op) commands against it.
//
// Stage 1 (v5.33.7+): static metrics — numbers seeded by demo-seed
// are echoed back on every tick without drift. Stage 2 will add
// realistic drift; stage 3 will add scripted scenarios.
//
// Env:
//   KVM_CENTER_DEMO_MONGO_URL   Mongo URI (REQUIRED)
//   KVM_CENTER_DEMO_MONGO_DB    db name (default: kvm-center-demo)
//   KVM_CENTER_DEMO_HOST_ID     which host's fixtures to claim (REQUIRED)
//   KVM_CENTER_SERVER           control plane URL (REQUIRED)
//   KVM_CENTER_AGENT_PORT       HTTP listen port (default: 8081)
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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"kvm-center/internal/models"
)

var (
	state     demoState
	stateMu   sync.RWMutex
	serverURL string
)

type demoState struct {
	host    *models.Host
	vms     []*models.VM
	storage []*models.StoragePool
}

func main() {
	mongoURI := mustEnv("KVM_CENTER_DEMO_MONGO_URL")
	mongoDB := envOr("KVM_CENTER_DEMO_MONGO_DB", "kvm-center-demo")
	hostID := mustEnv("KVM_CENTER_DEMO_HOST_ID")
	serverURL = mustEnv("KVM_CENTER_SERVER")
	agentPort, _ := strconv.Atoi(envOr("KVM_CENTER_AGENT_PORT", "8081"))

	log.Printf("demo-agent: host=%s  server=%s  port=%d", hostID, serverURL, agentPort)

	if err := loadFixtures(mongoURI, mongoDB, hostID); err != nil {
		log.Fatalf("load fixtures: %v", err)
	}

	// HTTP server for command callbacks.
	go startCommandServer(agentPort)

	// Initial tick immediately, then every 15 s.
	sendTelemetry()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			sendTelemetry()
		case s := <-sig:
			log.Printf("demo-agent: received %s, shutting down", s)
			return
		}
	}
}

// ─── Fixture load ──────────────────────────────────────────────

func loadFixtures(uri, dbName, hostID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cli, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return fmt.Errorf("mongo connect: %w", err)
	}
	defer cli.Disconnect(ctx)
	db := cli.Database(dbName)

	// Wait for the seed container to have populated the DB. Retry up
	// to ~2 min then give up. Without this the agent can race the
	// seeder and boot with no VMs.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var hostDoc bson.M
		err := db.Collection("hosts").FindOne(ctx, bson.M{"_id": hostID}).Decode(&hostDoc)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("host %s not seeded after 2 min: %w", hostID, err)
		}
		time.Sleep(2 * time.Second)
	}

	// Host.
	var hostDoc bson.M
	if err := db.Collection("hosts").FindOne(ctx, bson.M{"_id": hostID}).Decode(&hostDoc); err != nil {
		return fmt.Errorf("host %s: %w", hostID, err)
	}
	h := decodeHost(hostDoc)
	// VMs for this host.
	vmCur, err := db.Collection("vms").Find(ctx, bson.M{"hostid": hostID})
	if err != nil {
		return fmt.Errorf("vms find: %w", err)
	}
	var vms []*models.VM
	for vmCur.Next(ctx) {
		var doc bson.M
		if err := vmCur.Decode(&doc); err != nil {
			return err
		}
		vms = append(vms, decodeVM(doc))
	}
	vmCur.Close(ctx)

	// Storage pools for this host.
	stCur, err := db.Collection("storage").Find(ctx, bson.M{"hostid": hostID})
	if err != nil {
		return fmt.Errorf("storage find: %w", err)
	}
	var pools []*models.StoragePool
	for stCur.Next(ctx) {
		var doc bson.M
		if err := stCur.Decode(&doc); err != nil {
			return err
		}
		pools = append(pools, decodeStorage(doc))
	}
	stCur.Close(ctx)

	stateMu.Lock()
	state = demoState{host: h, vms: vms, storage: pools}
	stateMu.Unlock()
	log.Printf("demo-agent: loaded host=%s vms=%d storage_pools=%d", h.ID, len(vms), len(pools))
	return nil
}

// decodeHost / decodeVM / decodeStorage remarshal a bson.M through
// JSON into the typed model. The server doesn't have BSON tags on
// these types, so the driver's default lower-casing produces
// non-standard field names; JSON has the canonical names (snake_case).
// Round-tripping via JSON keeps the decoder trivial.
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

// remarshal normalises the driver's default lowercased keys into
// the snake_case JSON names the model expects. It iterates the
// bson.M, converts every key (e.g. "hostid") to the snake_case form
// ("host_id") by looking up the model's JSON tag, then marshals into
// the target. Done via a blunt field-name map — good enough for the
// demo's needs.
func remarshal(m bson.M, out any) {
	normalised := make(map[string]any, len(m))
	for k, v := range m {
		// Drop Mongo-internal keys.
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

// keyMap maps the driver's lowercased field names to the snake_case
// JSON keys on models. Covers only fields we actually read.
var keyMap = func() map[string]string {
	// If the key isn't explicitly mapped, pass it through as-is —
	// the json.Unmarshal will ignore unknown keys. Explicit entries
	// below rename ambiguous ones (e.g. "hostid" → "host_id").
	return map[string]string{
		"id":              "id",
		"ip":              "ip",
		"agentport":       "agent_port",
		"cpuusage":        "cpu_usage",
		"cpucount":        "cpu_count",
		"memusage":        "mem_usage",
		"memtotalmb":      "mem_total_mb",
		"memfreemb":       "mem_free_mb",
		"vmcount":         "vm_count",
		"status":          "status",
		"lastseen":        "last_seen",
		"os":              "os",
		"kernelver":       "kernel_ver",
		"libvirtver":      "libvirt_ver",
		"uptime":          "uptime",
		"agentversion":    "agent_version",
		"cpumodel":        "cpu_model",
		"sockets":         "sockets",
		"corespersocket":  "cores_per_socket",
		"threadspercore":  "threads_per_core",
		"numanodes":       "numa_nodes",
		"gpus":            "gpus",
		"name":            "name",
		"uuid":            "uuid",
		"hostid":          "host_id",
		"state":           "state",
		"disktotalgb":     "disk_total_gb",
		"diskusagegb":     "disk_usage_gb",
		"diskpath":        "disk_path",
		"disktype":        "disk_type",
		"cpumode":         "cpu_mode",
		"autostart":       "autostart",
		"bootdev":         "boot_dev",
		"firmware":        "firmware",
		"machinetype":     "machine_type",
		"videomodel":      "video_model",
		"snapshotcount":   "snapshot_count",
		"networks":        "networks",
		"kvmtoolsversion": "kvm_tools_version",
		"guestos":         "guest_os",
		"guestkernel":     "guest_kernel",
		"guesthostname":   "guest_hostname",
		"memusagemb":      "mem_usage_mb",
		"alldisks":        "all_disks",
		"hasqemuagent":    "has_qemu_agent",
		"snapshotsblockedreason": "snapshots_blocked_reason",
		// storage pool
		"path":         "path",
		"type":         "type",
		"capacitygb":   "capacity_gb",
		"allocationgb": "allocation_gb",
		"availablegb":  "available_gb",
		"persistent":   "persistent",
	}
}()

// ─── Telemetry ─────────────────────────────────────────────────

func sendTelemetry() {
	stateMu.RLock()
	h := state.host
	vms := append([]*models.VM(nil), state.vms...)
	storage := append([]*models.StoragePool(nil), state.storage...)
	stateMu.RUnlock()
	if h == nil {
		return
	}
	payload := models.TelemetryPayload{
		HostID:       h.ID,
		IP:           h.IP,
		AgentPort:    h.AgentPort,
		OS:           h.OS,
		KernelVer:    h.KernelVer,
		LibvirtVer:   h.LibvirtVer,
		CPUUsage:     h.CPUUsage,
		CPUCount:     h.CPUCount,
		MemUsage:     h.MemUsage,
		MemTotalMB:   h.MemTotalMB,
		MemFreeMB:    h.MemFreeMB,
		Uptime:       h.Uptime,
		AgentVersion: h.AgentVersion,
		CPUModel:     h.CPUModel,
		Sockets:      h.Sockets,
		CoresPerSocket: h.CoresPerSocket,
		ThreadsPerCore: h.ThreadsPerCore,
		NUMANodes:    h.NUMANodes,
		GPUs:         h.GPUs,
		Timestamp:    time.Now(),
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
		log.Printf("demo-agent: marshal: %v", err)
		return
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	client := &http.Client{Timeout: 10 * time.Second, Transport: tr}
	resp, err := client.Post(serverURL+"/api/telemetry", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("demo-agent: post: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("demo-agent: telemetry returned %d", resp.StatusCode)
	}
}

// ─── Command server ────────────────────────────────────────────

func startCommandServer(port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/command", handleCommand)
	mux.HandleFunc("/migrate-status", handleMigrateStatus)
	mux.HandleFunc("/cancel-migrate", handleCancel)
	mux.HandleFunc("/refresh-telemetry", handleRefresh)
	mux.HandleFunc("/snapshots/", handleSnapshots)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	addr := fmt.Sprintf(":%d", port)
	log.Printf("demo-agent: command server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("demo-agent: %v", err)
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

func handleRefresh(w http.ResponseWriter, _ *http.Request) {
	go sendTelemetry()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "refreshed": true})
}

// Returns fake snapshot list for any VM.
func handleSnapshots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	name := strings.TrimPrefix(r.URL.Path, "/snapshots/")
	// Generate 0-3 fake snapshots deterministically from the VM name.
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
