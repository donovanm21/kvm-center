// Command demo-seed populates a kvm-center Mongo backend with realistic
// fixture data so screenshots / walkthroughs / doc pages show a
// lived-in deployment instead of an empty shell. INTENDED FOR LOCAL
// DEV USE ONLY — the demo/ tree is stripped from the public GitHub
// mirror during the scrub.
//
// Scope (matches the user's "large deployment" pick, 2026-04-23):
//   - 30 hypervisors (hv-demo01-30.example.lan), mixed tiers
//     (small / medium / large — cores + RAM vary; three GPU-equipped
//     hosts), RFC5737 IPs 192.0.2.11-40
//   - ~150 VMs distributed across them (4-6 per host) — variety of
//     workload templates covering web / db / dev / CI / Windows /
//     ML / media
//   - 200 migrations across the last 30 days
//   - 800 audit events
//   - 4 snapshot policies (3 active, 1 paused)
//   - Notification config with webhook + email channels
//   - Command log with ~100 entries (incl. 3 "unknown" stranded for
//     the System Health problems card)
//   - Hypervisor certs (one per host, valid 10y)
//   - API tokens: 1 system-level admin token printed on stdout
//   - 3 users (admin, operator, viewer), 2 groups
//
// Run as a one-shot container inside the demo docker-compose stack.
// Env:
//
//	KVM_CENTER_DEMO_MONGO_URL  mongodb URI (REQUIRED)
//	KVM_CENTER_DEMO_MONGO_DB   db name (default: kvm-center-demo)
//	KVM_CENTER_DEMO_VERSION    string stamped as agent_version etc.
//	                           (default: dev)
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	mrand "math/rand"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"kvm-center/internal/models"
)

// Deterministic RNG so successive seed runs produce the same data —
// makes screenshots reproducible.
var rng = mrand.New(mrand.NewSource(20260423))

// Fixed reference "now" for all timestamps. Makes history
// relative-to-now regardless of when the seed is run.
var now = time.Now().UTC()

const (
	demoVersion  = "v5.33.7"
	adminUserID  = "user_demo_admin"
	opUserID     = "user_demo_operator"
	viewUserID   = "user_demo_viewer"
	adminGroupID = "group_demo_ops"
	viewGroupID  = "group_demo_viewers"
)

// Printed token — the seeder embeds this deterministic token
// (predictable for dev convenience; demo only).
const demoAPIToken = "kvmc_demo0001.00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func main() {
	uri := os.Getenv("KVM_CENTER_DEMO_MONGO_URL")
	if uri == "" {
		log.Fatal("KVM_CENTER_DEMO_MONGO_URL is required")
	}
	dbName := os.Getenv("KVM_CENTER_DEMO_MONGO_DB")
	if dbName == "" {
		dbName = "kvm-center-demo"
	}
	ver := os.Getenv("KVM_CENTER_DEMO_VERSION")
	if ver != "" {
		// used only in stamping; const demoVersion is the default
	}
	log.Printf("demo-seed: connecting to %s (db=%s)", redact(uri), dbName)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		log.Fatalf("mongo connect: %v", err)
	}
	defer client.Disconnect(ctx)
	if err := client.Ping(ctx, nil); err != nil {
		log.Fatalf("mongo ping: %v", err)
	}
	db := client.Database(dbName)

	// Drop every collection so re-seeding is idempotent.
	cols, err := db.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		log.Fatalf("list collections: %v", err)
	}
	for _, c := range cols {
		if err := db.Collection(c).Drop(ctx); err != nil {
			log.Printf("warn: drop %s: %v", c, err)
		}
	}
	log.Printf("demo-seed: wiped %d collections", len(cols))

	fx := buildFixtures()
	if err := writeFixtures(ctx, db, fx); err != nil {
		log.Fatalf("write: %v", err)
	}

	log.Printf("demo-seed: done")
	log.Printf("demo-seed: admin login — username=admin password=demo-password")
	log.Printf("demo-seed: API token (system) — %s", demoAPIToken)
}

func redact(uri string) string {
	if i := strings.Index(uri, "@"); i > 0 {
		if j := strings.Index(uri, "://"); j >= 0 && j < i {
			return uri[:j+3] + "****@" + uri[i+1:]
		}
	}
	return uri
}

// ─── Fixture shapes ────────────────────────────────────────────

type fixtures struct {
	hosts      []*models.Host
	vms        map[string][]*models.VM // hostID → VMs
	storage    map[string][]*models.StoragePool
	migrations []*models.MigrationRecord
	audit      []*models.AuditEvent
	users      map[string]*models.User
	groups     map[string]*models.Group
	tokens     map[string]*models.APIToken
	certs      map[string]*models.HypervisorCert
	policies   map[string]*models.SnapshotPolicy
	notif      *models.NotificationConfig
	features   models.Features
	cmdLog     map[string]*models.CommandLogEntry
}

func buildFixtures() *fixtures {
	fx := &fixtures{
		vms:      map[string][]*models.VM{},
		storage:  map[string][]*models.StoragePool{},
		users:    map[string]*models.User{},
		groups:   map[string]*models.Group{},
		tokens:   map[string]*models.APIToken{},
		certs:    map[string]*models.HypervisorCert{},
		policies: map[string]*models.SnapshotPolicy{},
		cmdLog:   map[string]*models.CommandLogEntry{},
	}
	buildHosts(fx)
	buildVMs(fx)
	buildStorage(fx)
	buildUsers(fx)
	buildGroups(fx)
	buildTokens(fx)
	buildCerts(fx)
	buildPolicies(fx)
	buildNotifications(fx)
	buildFeatures(fx)
	buildMigrations(fx)
	buildAudit(fx)
	buildCommandLog(fx)
	return fx
}

// ─── Hosts ─────────────────────────────────────────────────────

// Host tier templates. 30-host fleet is distributed across these
// three shapes to mimic a real deployment: mostly medium workhorses,
// a handful of small edge nodes, plus three beefy GPU hosts.
type hostTier struct {
	cpuCount, sockets, coresPerSocket, threadsPerCore, numaNodes int
	memGB                                                        int
	cpuModel                                                     string
	gpus                                                         []string
}

var (
	tierSmall = hostTier{
		cpuCount: 16, sockets: 1, coresPerSocket: 16, threadsPerCore: 1, numaNodes: 1,
		memGB:    64,
		cpuModel: "Intel Xeon E-2388G @ 3.20 GHz",
	}
	tierMedium = hostTier{
		cpuCount: 32, sockets: 1, coresPerSocket: 32, threadsPerCore: 2, numaNodes: 1,
		memGB:    128,
		cpuModel: "AMD EPYC 7543P 32-Core Processor",
	}
	tierLarge = hostTier{
		cpuCount: 64, sockets: 2, coresPerSocket: 32, threadsPerCore: 2, numaNodes: 2,
		memGB:    256,
		cpuModel: "AMD EPYC 7763 64-Core Processor",
	}
	tierGPU = hostTier{
		cpuCount: 48, sockets: 2, coresPerSocket: 24, threadsPerCore: 2, numaNodes: 2,
		memGB:    256,
		cpuModel: "AMD EPYC 7543 32-Core Processor",
		gpus: []string{
			"NVIDIA GeForce RTX 3090 (0000:41:00.0)",
			"NVIDIA GeForce RTX 3090 (0000:a1:00.0)",
		},
	}
)

// hostTierFor picks a tier for a given host index. Fleet shape:
//   - Hosts 1-3: GPU (for the ML/gaming workloads)
//   - Hosts 4-8: small edge nodes
//   - Hosts 9-27: medium workhorses (majority)
//   - Hosts 28-30: large compute boxes
func hostTierFor(idx int) hostTier {
	switch {
	case idx >= 1 && idx <= 3:
		return tierGPU
	case idx >= 4 && idx <= 8:
		return tierSmall
	case idx >= 28 && idx <= 30:
		return tierLarge
	default:
		return tierMedium
	}
}

const (
	demoHostCount = 30
	demoBaseIP4   = 10 // host 01 → 192.0.2.11
	demoBasePort  = 8100
)

func hostID(idx int) string { return fmt.Sprintf("hv-demo%02d.example.lan", idx) }

// host.IP is what the server uses to reach the agent + what the UI
// shows in the host card. In the demo we use a docker DNS alias
// (demo-agent-NN) so the server can reach the single demo-agents
// container. The UI displays this string verbatim — clearly flags
// the environment as a demo.
func hostIP(idx int) string { return fmt.Sprintf("demo-agent-%02d", idx) }
func hostPort(idx int) int  { return demoBasePort + idx }

func buildHosts(fx *fixtures) {
	for i := 1; i <= demoHostCount; i++ {
		t := hostTierFor(i)
		// CPU + mem usage: varied but bounded for a healthy fleet.
		cpuUse := 3.0 + rng.Float64()*35.0
		memUse := 20.0 + rng.Float64()*65.0
		if i%7 == 0 {
			// Occasional high-mem host (db workload).
			memUse = 75.0 + rng.Float64()*15.0
		}
		uptime := humanUptime(time.Duration(rng.Intn(90*24)) * time.Hour)
		h := &models.Host{
			ID:           hostID(i),
			IP:           hostIP(i),
			AgentPort:    hostPort(i),
			CPUUsage:     round1(cpuUse),
			CPUCount:     t.cpuCount,
			MemUsage:     round1(memUse),
			MemTotalMB:   uint64(t.memGB) * 1024,
			MemFreeMB:    uint64(float64(t.memGB*1024) * (1.0 - memUse/100.0)),
			Status:       models.HostOnline,
			LastSeen:     now.Add(-time.Duration(5+rng.Intn(20)) * time.Second),
			OS:           "Rocky Linux 9.7 (Blue Onyx)",
			KernelVer:    "5.14.0-544.el9.x86_64",
			LibvirtVer:   "virsh (libvirt) 10.10.0",
			Uptime:       uptime,
			AgentVersion: demoVersion,
			CPUModel:     t.cpuModel,
			Sockets:      t.sockets, CoresPerSocket: t.coresPerSocket,
			ThreadsPerCore: t.threadsPerCore, NUMANodes: t.numaNodes,
			GPUs: t.gpus,
		}
		fx.hosts = append(fx.hosts, h)
	}
}

func round1(f float64) float64 { return float64(int(f*10)) / 10 }

func humanUptime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// ─── VMs ───────────────────────────────────────────────────────

// vmTemplate describes one archetype. Per-host generation picks a
// set of templates based on the host's tier + picks realistic
// usage numbers around the template's baseline.
type vmTemplate struct {
	prefix, os, kernel, firmware string
	cpus                         int
	memMB                        uint64
	diskGB                       float64
	fillPct                      float64 // typical disk fill as fraction
	blockSnap                    bool
	runningBias                  float64 // probability the VM is running
	requiresGPU                  bool    // only placed on GPU-equipped hosts
	tags                         string  // unused today, room for later
}

var vmTemplates = []vmTemplate{
	{prefix: "web", os: "Rocky Linux 9.7 (Blue Onyx)", kernel: "5.14.0-544.el9.x86_64", firmware: "bios", cpus: 4, memMB: 8192, diskGB: 40, fillPct: 0.32, runningBias: 0.95},
	{prefix: "api", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", firmware: "efi", cpus: 4, memMB: 8192, diskGB: 50, fillPct: 0.45, runningBias: 0.95},
	{prefix: "db", os: "Rocky Linux 9.7 (Blue Onyx)", kernel: "5.14.0-544.el9.x86_64", firmware: "bios", cpus: 8, memMB: 32768, diskGB: 500, fillPct: 0.62, runningBias: 0.98},
	{prefix: "cache", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", firmware: "efi", cpus: 2, memMB: 16384, diskGB: 30, fillPct: 0.10, runningBias: 0.98},
	{prefix: "staging", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", firmware: "efi", cpus: 6, memMB: 16384, diskGB: 100, fillPct: 0.40, runningBias: 0.85},
	{prefix: "dev", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", firmware: "efi", cpus: 8, memMB: 16384, diskGB: 200, fillPct: 0.50, runningBias: 0.75},
	{prefix: "ci", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", firmware: "efi", cpus: 8, memMB: 16384, diskGB: 200, fillPct: 0.42, runningBias: 0.9},
	{prefix: "monitor", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", firmware: "efi", cpus: 4, memMB: 8192, diskGB: 120, fillPct: 0.40, runningBias: 1.0},
	{prefix: "registry", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", firmware: "efi", cpus: 2, memMB: 4096, diskGB: 500, fillPct: 0.35, runningBias: 1.0},
	{prefix: "media", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", firmware: "efi+secureboot", cpus: 4, memMB: 16384, diskGB: 4096, fillPct: 0.55, blockSnap: true, runningBias: 0.95},
	{prefix: "backup", os: "Rocky Linux 9.7 (Blue Onyx)", kernel: "5.14.0-544.el9.x86_64", firmware: "bios", cpus: 2, memMB: 4096, diskGB: 2048, fillPct: 0.55, runningBias: 0.6},
	{prefix: "legacy", os: "Rocky Linux 8.10 (Green Obsidian)", kernel: "4.18.0-553.el8.x86_64", firmware: "bios", cpus: 2, memMB: 4096, diskGB: 40, fillPct: 0.32, runningBias: 0.4},
	{prefix: "winsrv", os: "Microsoft Windows Server 2022", kernel: "10.0.20348", firmware: "efi+secureboot", cpus: 8, memMB: 32768, diskGB: 200, fillPct: 0.44, blockSnap: true, runningBias: 0.9},
	{prefix: "winclient", os: "Microsoft Windows 10 Pro", kernel: "10.0.19045", firmware: "efi+secureboot", cpus: 4, memMB: 16384, diskGB: 120, fillPct: 0.53, blockSnap: true, runningBias: 0.85},
	// GPU-only archetypes (placed only on GPU-equipped hosts).
	{prefix: "ml", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", firmware: "efi+secureboot", cpus: 16, memMB: 65536, diskGB: 1024, fillPct: 0.67, blockSnap: true, runningBias: 0.9, requiresGPU: true},
	{prefix: "gpu", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", firmware: "efi+secureboot", cpus: 16, memMB: 65536, diskGB: 500, fillPct: 0.45, blockSnap: true, runningBias: 0.85, requiresGPU: true},
}

const blockedReason = "UEFI pflash nvram is raw format — libvirt requires qcow2 nvram for internal snapshots. Convert with `qemu-img convert -O qcow2 <nvram>.fd <nvram>.qcow2`, then edit the domain XML so the <nvram> element uses format=\"qcow2\" and points at the new file."

// vmUUID builds a stable UUID so migrations + audit can reference
// them from a second pass without re-ordering worries.
func vmUUID(hostIdx, vmIdx int) string {
	return fmt.Sprintf("11111111-%04d-0000-0000-%012d", hostIdx, vmIdx)
}

func buildVMs(fx *fixtures) {
	// Distribution: 4-6 VMs per host. Avg 5 → ~150 VMs total.
	for i, h := range fx.hosts {
		hostIdx := i + 1
		vmCount := 4 + rng.Intn(3) // 4, 5, or 6
		// Candidate templates for this host — filter out GPU-only
		// archetypes if the host has no GPU.
		isGPU := len(h.GPUs) > 0
		candidates := make([]vmTemplate, 0, len(vmTemplates))
		for _, t := range vmTemplates {
			if t.requiresGPU && !isGPU {
				continue
			}
			candidates = append(candidates, t)
		}
		// Pick vmCount templates, tending to include 1 web + 1 db on
		// each host so the archetype distribution is realistic.
		chosen := pickTemplates(candidates, vmCount)
		for j, t := range chosen {
			vmIdx := j + 1
			uuid := vmUUID(hostIdx, vmIdx)
			name := fmt.Sprintf("vm-%s%02d-%02d", t.prefix, hostIdx, vmIdx)
			running := rng.Float64() < t.runningBias
			state := models.VMRunning
			if !running {
				state = models.VMStopped
			}
			// Disk usage: baseline fill ± 10%.
			fill := t.fillPct + (rng.Float64()-0.5)*0.2
			if fill < 0.05 {
				fill = 0.05
			}
			if fill > 0.95 {
				fill = 0.95
			}
			diskUseGB := t.diskGB * fill
			v := &models.VM{
				Name: name, UUID: uuid, HostID: h.ID, State: state,
				CPUCount: t.cpus, MemTotalMB: t.memMB,
				DiskTotalGB: t.diskGB, DiskUsageGB: round1(diskUseGB),
				DiskPath:      fmt.Sprintf("/var/lib/libvirt/images/%s.qcow2", name),
				DiskType:      "qcow2",
				CPUMode:       "host-passthrough",
				Autostart:     state == models.VMRunning,
				BootDev:       "hd",
				Firmware:      t.firmware,
				MachineType:   "pc-q35-rhel9.6.0",
				VideoModel:    "virtio",
				SnapshotCount: rng.Intn(5),
				Networks: []models.VMNetwork{{
					MAC:       fmt.Sprintf("52:54:00:%02x:%02x:%02x", rng.Intn(256), rng.Intn(256), rng.Intn(256)),
					Bridge:    "br0",
					Interface: fmt.Sprintf("vnet%d", rng.Intn(50)),
				}},
			}
			if state == models.VMRunning {
				v.MemUsageMB = uint64(float64(t.memMB) * (0.25 + rng.Float64()*0.55))
				v.CPUUsage = round1(0.8 + rng.Float64()*45.0)
				v.KVMToolsVersion = demoVersion
				v.GuestOS = t.os
				v.GuestKernel = t.kernel
				hostnameStem := strings.TrimSuffix(name, fmt.Sprintf("-%02d", vmIdx))
				v.GuestHostname = hostnameStem + ".example.lan"
				if strings.HasPrefix(t.prefix, "win") {
					v.GuestHostname = fmt.Sprintf("%s-%02d", strings.ToUpper(t.prefix), hostIdx)
				}
				v.GuestAgent = true
				v.GuestSource = "kvm-tools"
			}
			if t.blockSnap {
				v.SnapshotsBlockedReason = blockedReason
			}
			v.AllDisks = []models.VMDisk{{
				Target: "vda", Device: "disk", DiskType: "qcow2", Bus: "virtio",
				Path: v.DiskPath, CapacityGB: t.diskGB, AllocGB: v.DiskUsageGB,
			}}
			fx.vms[h.ID] = append(fx.vms[h.ID], v)
		}
		// Keep host.VMCount in sync — server recomputes from
		// telemetry but the seeded value shows on first render.
		h.VMCount = len(fx.vms[h.ID])
	}
}

// pickTemplates picks n templates with replacement. Biases slightly
// toward variety so you rarely get 5 "web" VMs on one host.
func pickTemplates(pool []vmTemplate, n int) []vmTemplate {
	out := make([]vmTemplate, 0, n)
	usedPrefix := map[string]int{}
	for len(out) < n {
		t := pool[rng.Intn(len(pool))]
		// Reject if the same archetype is already chosen twice.
		if usedPrefix[t.prefix] >= 2 {
			continue
		}
		usedPrefix[t.prefix]++
		out = append(out, t)
	}
	return out
}

// ─── Storage pools ─────────────────────────────────────────────

func buildStorage(fx *fixtures) {
	for _, h := range fx.hosts {
		// Sum allocated disk across this host's VMs to derive a
		// plausible storage usage number. Primary pool sizes come
		// from the host tier.
		var allocated float64
		for _, v := range fx.vms[h.ID] {
			allocated += v.DiskUsageGB
		}
		var primaryCapGB float64
		switch {
		case len(h.GPUs) > 0:
			primaryCapGB = 8192
		case h.CPUCount >= 64:
			primaryCapGB = 8192
		case h.CPUCount >= 32:
			primaryCapGB = 4096
		default:
			primaryCapGB = 2048
		}
		// Add 15-40% headroom over actual allocation to look lived-in.
		needed := allocated * (1.15 + rng.Float64()*0.25)
		if needed > primaryCapGB {
			primaryCapGB = roundToGB(needed)
		}
		avail := primaryCapGB - allocated
		fx.storage[h.ID] = []*models.StoragePool{
			{
				Name: "default", HostID: h.ID, State: "active", PoolType: "dir",
				Path:         "/var/lib/libvirt/images",
				CapacityGB:   primaryCapGB,
				AllocationGB: round1(allocated),
				AvailableGB:  round1(avail),
			},
		}
		// GPU + large hosts get an extra fast-nvme pool.
		if len(h.GPUs) > 0 || h.CPUCount >= 64 {
			fx.storage[h.ID] = append(fx.storage[h.ID], &models.StoragePool{
				Name: "nvme-fast", HostID: h.ID, State: "active", PoolType: "dir",
				Path:         "/mnt/nvme/images",
				CapacityGB:   2048,
				AllocationGB: round1(400 + rng.Float64()*800),
				AvailableGB:  round1(600 + rng.Float64()*800),
			})
		}
	}
}

func roundToGB(f float64) float64 {
	// Round up to the nearest 512 GB for capacity — mimics how real
	// pools are provisioned.
	step := 512.0
	return float64(int((f+step-1)/step)) * step
}

// ─── Users + Groups + Roles ────────────────────────────────────

func buildUsers(fx *fixtures) {
	adminHash, _ := hashPassword("demo-password")
	opHash, _ := hashPassword("demo-operator")
	viewHash, _ := hashPassword("demo-viewer")
	fx.users[adminUserID] = &models.User{
		ID: adminUserID, Username: "admin", Email: "admin@example.lan",
		PasswordHash: adminHash, Enabled: true,
		RoleIDs: []string{"role_admin"}, GroupIDs: []string{adminGroupID},
		CreatedAt: now.Add(-30 * 24 * time.Hour), LastLoginAt: now.Add(-3 * time.Minute),
	}
	fx.users[opUserID] = &models.User{
		ID: opUserID, Username: "operator", Email: "operator@example.lan",
		PasswordHash: opHash, Enabled: true,
		RoleIDs: []string{"role_operator"}, GroupIDs: []string{adminGroupID},
		CreatedAt: now.Add(-25 * 24 * time.Hour), LastLoginAt: now.Add(-4 * time.Hour),
	}
	fx.users[viewUserID] = &models.User{
		ID: viewUserID, Username: "viewer", Email: "viewer@example.lan",
		PasswordHash: viewHash, Enabled: true,
		RoleIDs: []string{"role_viewer"}, GroupIDs: []string{viewGroupID},
		CreatedAt: now.Add(-20 * 24 * time.Hour), LastLoginAt: now.Add(-2 * 24 * time.Hour),
	}
}

func buildGroups(fx *fixtures) {
	fx.groups[adminGroupID] = &models.Group{
		ID: adminGroupID, Name: "demo-ops", Description: "Demo operations team (admin + operator).",
		RoleIDs: []string{"role_operator"}, CreatedAt: now.Add(-30 * 24 * time.Hour),
	}
	fx.groups[viewGroupID] = &models.Group{
		ID: viewGroupID, Name: "demo-viewers", Description: "Demo read-only stakeholders.",
		RoleIDs: []string{"role_viewer"}, CreatedAt: now.Add(-30 * 24 * time.Hour),
	}
}

// ─── API tokens ────────────────────────────────────────────────

func buildTokens(fx *fixtures) {
	// Fixed token so the demo is predictable. Format:
	// kvmc_<id:8hex>.<secret:48hex>
	id := "demo0001"
	secret := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	sum := sha256.Sum256([]byte(secret))
	fx.tokens[id] = &models.APIToken{
		ID:         id,
		Name:       "Demo admin token",
		OwnerID:    adminUserID,
		Kind:       "system",
		Hash:       hex.EncodeToString(sum[:]),
		CreatedAt:  now.Add(-14 * 24 * time.Hour),
		LastUsedAt: now.Add(-5 * time.Minute),
	}
}

// ─── Hypervisor certs ──────────────────────────────────────────

func buildCerts(fx *fixtures) {
	for i, h := range fx.hosts {
		fx.certs[h.ID] = &models.HypervisorCert{
			HostID:      h.ID,
			SerialHex:   fmt.Sprintf("%032x", big.NewInt(int64(1000+i))),
			Fingerprint: fakeFingerprint(h.ID),
			Subject:     "CN=" + h.ID,
			NotBefore:   now.Add(-30 * 24 * time.Hour),
			NotAfter:    now.Add(10 * 365 * 24 * time.Hour),
			IssuedAt:    now.Add(-30 * 24 * time.Hour),
			IssuedBy:    adminUserID,
		}
	}
}

func fakeFingerprint(seed string) string {
	h := sha256.Sum256([]byte("demo-" + seed))
	parts := make([]string, len(h))
	for i, b := range h {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":")
}

// ─── Snapshot policies ─────────────────────────────────────────

func buildPolicies(fx *fixtures) {
	// Pick VMs by archetype name prefix — the generated pool has
	// variable naming but we can filter by whether name contains
	// the archetype's prefix.
	pickByPrefix := func(prefix string, max int) []string {
		var out []string
		for _, vms := range fx.vms {
			for _, v := range vms {
				if strings.Contains(v.Name, "vm-"+prefix) {
					out = append(out, v.UUID)
					if len(out) >= max {
						return out
					}
				}
			}
		}
		return out
	}
	// Active hourly: web tier — cover up to 8 web VMs.
	fx.policies["pol_demo_web_hourly"] = &models.SnapshotPolicy{
		ID: "pol_demo_web_hourly", Name: "Web tier — hourly",
		Enabled:      true,
		VMUUIDs:      pickByPrefix("web", 8),
		EveryMinutes: 60, RetentionKeep: 12,
		CreatedAt:    now.Add(-21 * 24 * time.Hour),
		LastRunAt:    now.Add(-12 * time.Minute),
		LastRunCount: 8,
	}
	// Active daily: db tier.
	fx.policies["pol_demo_db_daily"] = &models.SnapshotPolicy{
		ID: "pol_demo_db_daily", Name: "Database — daily",
		Enabled:      true,
		VMUUIDs:      pickByPrefix("db", 8),
		EveryMinutes: 1440, RetentionKeep: 7,
		CreatedAt:    now.Add(-45 * 24 * time.Hour),
		LastRunAt:    now.Add(-7 * time.Hour),
		LastRunCount: 6,
	}
	// Active weekly: api tier.
	fx.policies["pol_demo_api_weekly"] = &models.SnapshotPolicy{
		ID: "pol_demo_api_weekly", Name: "API tier — weekly",
		Enabled:      true,
		VMUUIDs:      pickByPrefix("api", 10),
		EveryMinutes: 10080, RetentionKeep: 4,
		CreatedAt:    now.Add(-10 * 24 * time.Hour),
		LastRunAt:    now.Add(-3 * 24 * time.Hour),
		LastRunCount: 9,
	}
	// Paused: old weekly policy the operator disabled.
	fx.policies["pol_demo_weekly_paused"] = &models.SnapshotPolicy{
		ID: "pol_demo_weekly_paused", Name: "Weekly full (paused)",
		Enabled:      false,
		VMUUIDs:      pickByPrefix("staging", 3),
		EveryMinutes: 10080, RetentionKeep: 4,
		CreatedAt:    now.Add(-60 * 24 * time.Hour),
		LastRunAt:    now.Add(-10 * 24 * time.Hour),
		LastRunCount: 1,
	}
}

// ─── Notifications ─────────────────────────────────────────────

func buildNotifications(fx *fixtures) {
	fx.notif = &models.NotificationConfig{
		Channels: []*models.NotificationChannel{
			{ID: "ch_demo_slack", Name: "Slack — ops",
				Kind: "webhook", Enabled: true,
				WebhookURL:    "https://hooks.example.lan/T000/B000/XXX",
				WebhookFormat: "slack",
				LastSendAt:    now.Add(-23 * time.Minute).Format(time.RFC3339),
				LastSendCount: 47,
			},
			{ID: "ch_demo_pd", Name: "PagerDuty — critical",
				Kind: "webhook", Enabled: true,
				WebhookURL:    "https://events.pagerduty.com/v2/enqueue",
				WebhookFormat: "generic",
				EventTypes: map[string]bool{
					"host.offline":     true,
					"migration.failed": true,
					"tls.expiring":     true,
					"ca.expiring":      true,
					"vm.crashed":       true,
				},
				LastSendAt:    now.Add(-3 * time.Hour).Format(time.RFC3339),
				LastSendCount: 3,
			},
			{ID: "ch_demo_email", Name: "Ops email",
				Kind: "email", Enabled: false,
				EmailTo: "ops@example.lan", EmailFrom: "kvm-center@example.lan",
			},
		},
		EventTypes: map[string]bool{
			"vm.crashed":               true,
			"vm.recovered":             true,
			"host.offline":             true,
			"host.recovered":           true,
			"migration.complete":       false,
			"migration.failed":         true,
			"tls.expiring":             true,
			"ca.expiring":              true,
			"hypervisor.cert.expiring": true,
			"snapshot.policy.failed":   true,
		},
		SMTPHost: "smtp.example.lan", SMTPPort: 587,
		SMTPFrom: "kvm-center@example.lan", SMTPStartTLS: true,
	}
}

// ─── Features ──────────────────────────────────────────────────

func buildFeatures(fx *fixtures) {
	fx.features = models.Features{
		PublicURL:        "https://demo.example.lan",
		SetupComplete:    true,
		SetupCompletedAt: now.Add(-60 * 24 * time.Hour),
	}
}

// ─── Migrations (30 days, ~200 records) ────────────────────────

func buildMigrations(fx *fixtures) {
	// Flatten the current fleet into (vmName, uuid, host) tuples so
	// we can pick a random VM and move it to any OTHER host.
	type vmEntry struct{ uuid, name, host string }
	var pool []vmEntry
	for _, vms := range fx.vms {
		for _, v := range vms {
			pool = append(pool, vmEntry{v.UUID, v.Name, v.HostID})
		}
	}
	hostIDs := make([]string, 0, len(fx.hosts))
	for _, h := range fx.hosts {
		hostIDs = append(hostIDs, h.ID)
	}
	// 200 migrations across the last 30 days.
	for i := 0; i < 200; i++ {
		vm := pool[rng.Intn(len(pool))]
		dst := hostIDs[rng.Intn(len(hostIDs))]
		for dst == vm.host {
			dst = hostIDs[rng.Intn(len(hostIDs))]
		}
		age := time.Duration(rng.Intn(30*24)) * time.Hour
		startedAt := now.Add(-age).Unix()
		mode := "live"
		if rng.Intn(3) == 0 {
			mode = "offline"
		}
		transport := "tls"
		if rng.Intn(5) == 0 {
			transport = "ssh"
		}
		status := "complete"
		var errMsg string
		switch rng.Intn(10) {
		case 0:
			status = "failed"
			errMsg = "libvirtd destination refused connection"
		case 1:
			status = "cancelled"
			errMsg = "cancelled by operator"
		}
		elapsed := int64(60 + rng.Intn(300))
		rec := &models.MigrationRecord{
			ID:          fmt.Sprintf("%s-%d-%04x", vm.name, startedAt, i),
			VMName:      vm.name,
			SourceHost:  vm.host,
			DestHost:    dst,
			Mode:        mode,
			Transport:   transport,
			Status:      status,
			StartedAt:   startedAt,
			CompletedAt: startedAt + elapsed,
			ElapsedSec:  elapsed,
			Progress:    100,
			Phase:       status,
			Error:       errMsg,
		}
		if status == "failed" || status == "cancelled" {
			rec.Progress = 25 + rng.Intn(60)
		}
		fx.migrations = append(fx.migrations, rec)
	}
}

// ─── Audit events (~500) ───────────────────────────────────────

func buildAudit(fx *fixtures) {
	actors := []struct{ id, name string }{
		{adminUserID, "admin"},
		{opUserID, "operator"},
		{viewUserID, "viewer"},
	}
	actions := []struct {
		action, resource string
		success          bool
	}{
		{"vm.action", "vm/%s", true},
		{"vm.snapshot", "vm/%s", true},
		{"vm.snapshot.delete", "vm/%s", true},
		{"vm.console", "vm/%s", true},
		{"vm.edit", "vm/%s", true},
		{"migration.start", "vm/%s", true},
		{"auth.login", "user/%s", true},
		{"auth.logout", "user/%s", true},
		{"auth.login.fail", "user/%s", false},
		{"token.create", "token/%s", true},
		{"role.update", "role/role_operator", true},
		{"settings.update", "settings/notifications", true},
		{"host.reconcile", "host/%s", true},
		{"grants.rotate", "grant-signer", true},
		{"command.stranded", "vm/%s", false},
	}
	vmUUIDs := []string{}
	for _, vms := range fx.vms {
		for _, v := range vms {
			vmUUIDs = append(vmUUIDs, v.UUID)
		}
	}
	hostIDs := make([]string, 0, len(fx.hosts))
	for _, h := range fx.hosts {
		hostIDs = append(hostIDs, h.ID)
	}
	for i := 0; i < 800; i++ {
		a := actions[rng.Intn(len(actions))]
		actor := actors[rng.Intn(len(actors))]
		var resource string
		switch {
		case strings.HasPrefix(a.resource, "vm/"):
			resource = "vm/" + vmUUIDs[rng.Intn(len(vmUUIDs))]
		case strings.HasPrefix(a.resource, "host/"):
			resource = "host/" + hostIDs[rng.Intn(len(hostIDs))]
		case strings.HasPrefix(a.resource, "user/"):
			resource = "user/" + actor.id
		case strings.HasPrefix(a.resource, "token/"):
			resource = "token/demo0001"
		default:
			resource = a.resource
		}
		at := now.Add(-time.Duration(rng.Intn(30*24*60)) * time.Minute)
		ev := &models.AuditEvent{
			ID:        fmt.Sprintf("evt_%016x", rng.Int63()),
			At:        at,
			ActorID:   actor.id,
			ActorName: actor.name,
			ActorIP:   fmt.Sprintf("192.0.2.%d", 20+rng.Intn(30)),
			Action:    a.action,
			Resource:  resource,
			Success:   a.success,
		}
		if !a.success {
			ev.Error = "see context"
		}
		fx.audit = append(fx.audit, ev)
	}
}

// ─── Command log (~100 incl. 3 stranded) ───────────────────────

func buildCommandLog(fx *fixtures) {
	type vmEntry struct{ uuid, name, host string }
	var pool []vmEntry
	for _, vms := range fx.vms {
		for _, v := range vms {
			pool = append(pool, vmEntry{v.UUID, v.Name, v.HostID})
		}
	}
	actions := []string{"START", "STOP", "RESTART", "SNAPSHOT", "POWER_CYCLE", "EDIT_VM"}
	for i := 0; i < 97; i++ {
		vm := pool[rng.Intn(len(pool))]
		act := actions[rng.Intn(len(actions))]
		reqID := fmt.Sprintf("req_%s", randHex(16))
		submitted := now.Add(-time.Duration(rng.Intn(7*24*60)) * time.Minute)
		applied := submitted.Add(time.Duration(100+rng.Intn(2500)) * time.Millisecond)
		outcome := "success"
		status := 200
		msg := "OK"
		if rng.Intn(12) == 0 {
			outcome = "failed"
			status = 502
			msg = "agent unreachable"
		}
		expires := applied.Add(30 * 24 * time.Hour)
		fx.cmdLog[reqID] = &models.CommandLogEntry{
			RequestID: reqID, Action: act, HostID: vm.host, VMUUID: vm.uuid, VMName: vm.name,
			ActorID: adminUserID, SubmittedAt: submitted, AppliedAt: &applied,
			Outcome: outcome, StatusCode: status, Message: msg, ExpiresAt: &expires,
		}
	}
	// 3 stranded "unknown" entries → flagged on the Health problems card.
	for i := 0; i < 3; i++ {
		vm := pool[rng.Intn(len(pool))]
		reqID := fmt.Sprintf("req_%s", randHex(16))
		submitted := now.Add(-time.Duration(60+rng.Intn(300)) * time.Minute)
		applied := submitted.Add(4 * time.Hour)
		expires := applied.Add(30 * 24 * time.Hour)
		fx.cmdLog[reqID] = &models.CommandLogEntry{
			RequestID: reqID, Action: "MIGRATE", HostID: vm.host, VMUUID: vm.uuid, VMName: vm.name,
			ActorID: adminUserID, SubmittedAt: submitted, AppliedAt: &applied,
			Outcome: "unknown", StatusCode: 0,
			Message:   "server restarted while command was in flight; outcome undetermined - check the target VM state and resolve manually",
			ExpiresAt: &expires,
		}
	}
}

// ─── Write to Mongo ────────────────────────────────────────────

func writeFixtures(ctx context.Context, db *mongo.Database, fx *fixtures) error {
	type col struct {
		name string
		docs []any
	}
	cols := []col{}

	// hosts
	var hostDocs []any
	for _, h := range fx.hosts {
		hostDocs = append(hostDocs, inlineDoc(h.ID, h))
	}
	cols = append(cols, col{"hosts", hostDocs})

	// vms (flat) — _id on uuid
	var vmDocs []any
	for _, vms := range fx.vms {
		for _, v := range vms {
			vmDocs = append(vmDocs, inlineDoc(v.UUID, v))
		}
	}
	cols = append(cols, col{"vms", vmDocs})

	// storage — _id on host_id+name
	var storageDocs []any
	for hostID, pools := range fx.storage {
		for _, p := range pools {
			id := hostID + "/" + p.Name
			storageDocs = append(storageDocs, inlineDoc(id, p))
		}
	}
	cols = append(cols, col{"storage", storageDocs})

	// migrations
	var migDocs []any
	for _, m := range fx.migrations {
		migDocs = append(migDocs, inlineDoc(m.ID, m))
	}
	cols = append(cols, col{"migrations", migDocs})

	// audit events — no _id override (Mongo generates)
	var auditDocs []any
	for _, e := range fx.audit {
		auditDocs = append(auditDocs, inlineDoc(e.ID, e))
	}
	cols = append(cols, col{"audit_events", auditDocs})

	// users/groups
	var userDocs []any
	for _, u := range fx.users {
		userDocs = append(userDocs, inlineDoc(u.ID, u))
	}
	cols = append(cols, col{"users", userDocs})

	var groupDocs []any
	for _, g := range fx.groups {
		groupDocs = append(groupDocs, inlineDoc(g.ID, g))
	}
	cols = append(cols, col{"groups", groupDocs})

	// tokens
	var tokDocs []any
	for _, t := range fx.tokens {
		tokDocs = append(tokDocs, inlineDoc(t.ID, t))
	}
	cols = append(cols, col{"api_tokens", tokDocs})

	// hypervisor certs
	var certDocs []any
	for _, c := range fx.certs {
		certDocs = append(certDocs, inlineDoc(c.HostID, c))
	}
	cols = append(cols, col{"hypervisor_certs", certDocs})

	// snapshot policies
	var polDocs []any
	for _, p := range fx.policies {
		polDocs = append(polDocs, inlineDoc(p.ID, p))
	}
	cols = append(cols, col{"snapshot_policies", polDocs})

	// command log
	var clDocs []any
	for _, e := range fx.cmdLog {
		clDocs = append(clDocs, e)
	}
	cols = append(cols, col{"command_log", clDocs})

	// Singletons: notifications, features, server_meta.
	consoleSecret := hex.EncodeToString(randBytes(32))
	cols = append(cols, col{"notifications", []any{
		struct {
			ID                         string `bson:"_id"`
			*models.NotificationConfig `bson:",inline"`
		}{"singleton", fx.notif},
	}})
	cols = append(cols, col{"features", []any{
		struct {
			ID              string `bson:"_id"`
			models.Features `bson:",inline"`
		}{"singleton", fx.features},
	}})
	cols = append(cols, col{"server_meta", []any{
		bson.M{"_id": "singleton", "console_secret": consoleSecret},
	}})

	// Insert per collection.
	for _, c := range cols {
		if len(c.docs) == 0 {
			continue
		}
		if _, err := db.Collection(c.name).InsertMany(ctx, c.docs); err != nil {
			return fmt.Errorf("insert %s: %w", c.name, err)
		}
		log.Printf("demo-seed: inserted %d into %s", len(c.docs), c.name)
	}
	return nil
}

// inlineDoc wraps a typed model into the BSON shape the server
// writes: `{ _id: id, <model fields inlined> }`. Marshals the model
// to BSON, converts to a bson.M, injects _id. Matches the server's
// `struct { ID string bson:"_id"; *Model bson:",inline" }` pattern
// without needing a wrapper struct per model type.
func inlineDoc(id string, v any) any {
	data, err := bson.Marshal(v)
	if err != nil {
		log.Fatalf("bson.Marshal: %v", err)
	}
	var m bson.M
	if err := bson.Unmarshal(data, &m); err != nil {
		log.Fatalf("bson.Unmarshal: %v", err)
	}
	m["_id"] = id
	return m
}

// ─── Password hash + random helpers (self-contained) ──────────

const (
	pbkdf2Iterations = 200_000
	pbkdf2KeyLen     = 32
	pbkdf2SaltLen    = 16
)

func hashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2([]byte(password), salt, pbkdf2Iterations, pbkdf2KeyLen)
	return fmt.Sprintf("pbkdf2$sha256$%d$%s$%s",
		pbkdf2Iterations,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(key)), nil
}

func pbkdf2(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, numBlocks*hashLen)
	buf := make([]byte, 4)
	for block := 1; block <= numBlocks; block++ {
		binary.BigEndian.PutUint32(buf, uint32(block))
		prf.Reset()
		prf.Write(salt)
		prf.Write(buf)
		u := prf.Sum(nil)
		t := make([]byte, hashLen)
		copy(t, u)
		for i := 2; i <= iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func randHex(n int) string {
	return hex.EncodeToString(randBytes(n))
}
