// Command demo-seed populates a kvm-center Mongo backend with realistic
// fixture data so screenshots / walkthroughs / doc pages show a
// lived-in deployment instead of an empty shell. INTENDED FOR LOCAL
// DEV USE ONLY — the demo/ tree is stripped from the public GitHub
// mirror during the scrub.
//
// Scope (matches the user's "large" pick, 2026-04-23):
//   - 3 hypervisors (hv-demo01/02/03.example.lan, RFC5737 IPs)
//   - 20 VMs across them (mix of Linux + Windows, various states)
//   - ~100 migrations across the last 30 days (complete / failed / cancelled)
//   - ~500 audit events
//   - 3 snapshot policies (2 active, 1 paused)
//   - Notification config with webhook + email channels
//   - Command log with ~50 entries (incl. 3 "unknown" stranded for
//     the System Health problems card)
//   - Hypervisor certs (one per host, valid 10y)
//   - API tokens: 1 system-level admin token printed on stdout
//   - 3 users (admin, operator, viewer), 2 groups
//
// Run as a one-shot container inside the demo docker-compose stack.
// Env:
//   KVM_CENTER_DEMO_MONGO_URL  mongodb URI (REQUIRED)
//   KVM_CENTER_DEMO_MONGO_DB   db name (default: kvm-center-demo)
//   KVM_CENTER_DEMO_VERSION    string stamped as agent_version etc.
//                              (default: dev)
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

func buildHosts(fx *fixtures) {
	fx.hosts = []*models.Host{
		{
			ID: "hv-demo01.example.lan", IP: "192.0.2.11", AgentPort: 8081,
			CPUUsage: 18.4, CPUCount: 32, MemUsage: 62.1, MemTotalMB: 128 * 1024, MemFreeMB: 48 * 1024,
			VMCount: 7, Status: models.HostOnline, LastSeen: now.Add(-12 * time.Second),
			OS: "Rocky Linux 9.7 (Blue Onyx)", KernelVer: "5.14.0-544.el9.x86_64",
			LibvirtVer: "virsh (libvirt) 10.10.0", Uptime: "17d 4h 23m", AgentVersion: demoVersion,
			CPUModel: "AMD EPYC 7543P 32-Core Processor",
			Sockets:  1, CoresPerSocket: 32, ThreadsPerCore: 2, NUMANodes: 1,
		},
		{
			ID: "hv-demo02.example.lan", IP: "192.0.2.12", AgentPort: 8081,
			CPUUsage: 7.2, CPUCount: 32, MemUsage: 41.8, MemTotalMB: 128 * 1024, MemFreeMB: 74 * 1024,
			VMCount: 7, Status: models.HostOnline, LastSeen: now.Add(-8 * time.Second),
			OS: "Rocky Linux 9.7 (Blue Onyx)", KernelVer: "5.14.0-544.el9.x86_64",
			LibvirtVer: "virsh (libvirt) 10.10.0", Uptime: "45d 12h 1m", AgentVersion: demoVersion,
			CPUModel: "AMD EPYC 7543P 32-Core Processor",
			Sockets:  1, CoresPerSocket: 32, ThreadsPerCore: 2, NUMANodes: 1,
		},
		{
			ID: "hv-demo03.example.lan", IP: "192.0.2.13", AgentPort: 8081,
			CPUUsage: 33.9, CPUCount: 48, MemUsage: 78.5, MemTotalMB: 256 * 1024, MemFreeMB: 55 * 1024,
			VMCount: 6, Status: models.HostOnline, LastSeen: now.Add(-14 * time.Second),
			OS: "Rocky Linux 9.7 (Blue Onyx)", KernelVer: "5.14.0-544.el9.x86_64",
			LibvirtVer: "virsh (libvirt) 10.10.0", Uptime: "3d 2h 7m", AgentVersion: demoVersion,
			CPUModel: "AMD EPYC 7543P 32-Core Processor",
			Sockets:  2, CoresPerSocket: 24, ThreadsPerCore: 2, NUMANodes: 2,
			GPUs: []string{"NVIDIA GeForce RTX 3090 (0000:41:00.0)", "NVIDIA GeForce RTX 3090 (0000:a1:00.0)"},
		},
	}
}

// ─── VMs ───────────────────────────────────────────────────────

type vmDef struct {
	name, uuid, host, os, kernel, hostname string
	state                                  models.VMState
	cpus                                   int
	memMB                                  uint64
	diskGB                                 float64
	diskUseGB                              float64
	firmware                               string
	blockSnap                              bool // SnapshotsBlockedReason set
	snapCount                              int
	noKVMTools                             bool
}

func buildVMs(fx *fixtures) {
	defs := []vmDef{
		// hv-demo01 (7 VMs)
		{name: "vm-web01", uuid: "11111111-0001-0000-0000-000000000001", host: "hv-demo01.example.lan", os: "Rocky Linux 9.7 (Blue Onyx)", kernel: "5.14.0-544.el9.x86_64", hostname: "vm-web01.example.lan", state: models.VMRunning, cpus: 4, memMB: 8192, diskGB: 40, diskUseGB: 14.2, firmware: "bios", snapCount: 3},
		{name: "vm-api01", uuid: "11111111-0001-0000-0000-000000000002", host: "hv-demo01.example.lan", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", hostname: "vm-api01.example.lan", state: models.VMRunning, cpus: 4, memMB: 8192, diskGB: 50, diskUseGB: 21.8, firmware: "efi", snapCount: 2},
		{name: "vm-db01", uuid: "11111111-0001-0000-0000-000000000003", host: "hv-demo01.example.lan", os: "Rocky Linux 9.7 (Blue Onyx)", kernel: "5.14.0-544.el9.x86_64", hostname: "vm-db01.example.lan", state: models.VMRunning, cpus: 8, memMB: 32768, diskGB: 500, diskUseGB: 312.4, firmware: "bios", snapCount: 5},
		{name: "vm-cache01", uuid: "11111111-0001-0000-0000-000000000004", host: "hv-demo01.example.lan", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", hostname: "vm-cache01.example.lan", state: models.VMRunning, cpus: 2, memMB: 16384, diskGB: 30, diskUseGB: 3.1, firmware: "efi"},
		{name: "vm-monitor", uuid: "11111111-0001-0000-0000-000000000005", host: "hv-demo01.example.lan", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", hostname: "grafana.example.lan", state: models.VMRunning, cpus: 4, memMB: 8192, diskGB: 120, diskUseGB: 47.3, firmware: "efi", snapCount: 1},
		{name: "vm-ci01", uuid: "11111111-0001-0000-0000-000000000006", host: "hv-demo01.example.lan", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", hostname: "ci01.example.lan", state: models.VMRunning, cpus: 8, memMB: 16384, diskGB: 200, diskUseGB: 84.6, firmware: "efi", snapCount: 2},
		{name: "vm-legacy", uuid: "11111111-0001-0000-0000-000000000007", host: "hv-demo01.example.lan", os: "Rocky Linux 8.10 (Green Obsidian)", kernel: "4.18.0-553.el8.x86_64", hostname: "legacy.example.lan", state: models.VMStopped, cpus: 2, memMB: 4096, diskGB: 40, diskUseGB: 12.5, firmware: "bios", noKVMTools: true},

		// hv-demo02 (7 VMs)
		{name: "vm-web02", uuid: "11111111-0002-0000-0000-000000000001", host: "hv-demo02.example.lan", os: "Rocky Linux 9.7 (Blue Onyx)", kernel: "5.14.0-544.el9.x86_64", hostname: "vm-web02.example.lan", state: models.VMRunning, cpus: 4, memMB: 8192, diskGB: 40, diskUseGB: 11.9, firmware: "bios", snapCount: 3},
		{name: "vm-api02", uuid: "11111111-0002-0000-0000-000000000002", host: "hv-demo02.example.lan", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", hostname: "vm-api02.example.lan", state: models.VMRunning, cpus: 4, memMB: 8192, diskGB: 50, diskUseGB: 22.4, firmware: "efi", snapCount: 2},
		{name: "vm-db02", uuid: "11111111-0002-0000-0000-000000000003", host: "hv-demo02.example.lan", os: "Rocky Linux 9.7 (Blue Onyx)", kernel: "5.14.0-544.el9.x86_64", hostname: "vm-db02.example.lan", state: models.VMRunning, cpus: 8, memMB: 32768, diskGB: 500, diskUseGB: 298.7, firmware: "bios", snapCount: 4},
		{name: "vm-staging", uuid: "11111111-0002-0000-0000-000000000004", host: "hv-demo02.example.lan", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", hostname: "staging.example.lan", state: models.VMRunning, cpus: 6, memMB: 16384, diskGB: 100, diskUseGB: 41.2, firmware: "efi", snapCount: 7},
		{name: "vm-registry", uuid: "11111111-0002-0000-0000-000000000005", host: "hv-demo02.example.lan", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", hostname: "registry.example.lan", state: models.VMRunning, cpus: 2, memMB: 4096, diskGB: 500, diskUseGB: 187.5, firmware: "efi"},
		{name: "vm-backup", uuid: "11111111-0002-0000-0000-000000000006", host: "hv-demo02.example.lan", os: "Rocky Linux 9.7 (Blue Onyx)", kernel: "5.14.0-544.el9.x86_64", hostname: "backup.example.lan", state: models.VMStopped, cpus: 2, memMB: 4096, diskGB: 2048, diskUseGB: 1128.0, firmware: "bios"},
		{name: "vm-media", uuid: "11111111-0002-0000-0000-000000000007", host: "hv-demo02.example.lan", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", hostname: "media.example.lan", state: models.VMRunning, cpus: 4, memMB: 16384, diskGB: 4096, diskUseGB: 2314.7, firmware: "efi+secureboot", blockSnap: true},

		// hv-demo03 (6 VMs incl. Windows)
		{name: "vm-web03", uuid: "11111111-0003-0000-0000-000000000001", host: "hv-demo03.example.lan", os: "Rocky Linux 9.7 (Blue Onyx)", kernel: "5.14.0-544.el9.x86_64", hostname: "vm-web03.example.lan", state: models.VMRunning, cpus: 4, memMB: 8192, diskGB: 40, diskUseGB: 12.7, firmware: "bios", snapCount: 3},
		{name: "vm-gpu01", uuid: "11111111-0003-0000-0000-000000000002", host: "hv-demo03.example.lan", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", hostname: "gpu01.example.lan", state: models.VMRunning, cpus: 16, memMB: 65536, diskGB: 500, diskUseGB: 217.4, firmware: "efi+secureboot", blockSnap: true},
		{name: "vm-ml01", uuid: "11111111-0003-0000-0000-000000000003", host: "hv-demo03.example.lan", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", hostname: "ml01.example.lan", state: models.VMRunning, cpus: 16, memMB: 65536, diskGB: 1024, diskUseGB: 687.9, firmware: "efi+secureboot", blockSnap: true},
		{name: "vm-dev01", uuid: "11111111-0003-0000-0000-000000000004", host: "hv-demo03.example.lan", os: "Ubuntu 24.04.4 LTS", kernel: "6.8.0-51-generic", hostname: "dev01.example.lan", state: models.VMRunning, cpus: 8, memMB: 16384, diskGB: 200, diskUseGB: 91.3, firmware: "efi", snapCount: 2},
		{name: "vm-winsrv", uuid: "11111111-0003-0000-0000-000000000005", host: "hv-demo03.example.lan", os: "Microsoft Windows Server 2022", kernel: "10.0.20348", hostname: "WIN-DEMO01", state: models.VMRunning, cpus: 8, memMB: 32768, diskGB: 200, diskUseGB: 87.3, firmware: "efi+secureboot", blockSnap: true},
		{name: "vm-winclient", uuid: "11111111-0003-0000-0000-000000000006", host: "hv-demo03.example.lan", os: "Microsoft Windows 10 Pro", kernel: "10.0.19045", hostname: "DESKTOP-DEMO01", state: models.VMRunning, cpus: 4, memMB: 16384, diskGB: 120, diskUseGB: 64.8, firmware: "efi+secureboot", blockSnap: true},
	}

	blockedReason := "UEFI pflash nvram is raw format — libvirt requires qcow2 nvram for internal snapshots. Convert with `qemu-img convert -O qcow2 <nvram>.fd <nvram>.qcow2`, then edit the domain XML so the <nvram> element uses format=\"qcow2\" and points at the new file."

	for _, d := range defs {
		v := &models.VM{
			Name: d.name, UUID: d.uuid, HostID: d.host, State: d.state,
			CPUCount: d.cpus, MemTotalMB: d.memMB, DiskTotalGB: d.diskGB, DiskUsageGB: d.diskUseGB,
			DiskPath: fmt.Sprintf("/var/lib/libvirt/images/%s.qcow2", d.name), DiskType: "qcow2",
			CPUMode: "host-passthrough", Autostart: d.state == models.VMRunning,
			BootDev:     "hd",
			Firmware:    d.firmware,
			MachineType: "pc-q35-rhel9.6.0",
			VideoModel:  "virtio",
			SnapshotCount: d.snapCount,
			Networks: []models.VMNetwork{
				{MAC: fmt.Sprintf("52:54:00:%02x:%02x:%02x", rng.Intn(256), rng.Intn(256), rng.Intn(256)),
					Bridge: "br0", Interface: fmt.Sprintf("vnet%d", rng.Intn(50))},
			},
		}
		if d.state == models.VMRunning {
			v.MemUsageMB = uint64(float64(d.memMB) * (0.3 + rng.Float64()*0.5))
			v.CPUUsage = 1.5 + rng.Float64()*40
			if !d.noKVMTools {
				v.KVMToolsVersion = demoVersion
				v.GuestOS = d.os
				v.GuestKernel = d.kernel
				v.GuestHostname = d.hostname
			}
		}
		if d.blockSnap {
			v.SnapshotsBlockedReason = blockedReason
		}
		if !d.noKVMTools && d.state == models.VMRunning {
			v.GuestAgent = true
			v.GuestSource = "kvm-tools"
		}
		// One primary disk entry.
		v.AllDisks = []models.VMDisk{{
			Target: "vda", Device: "disk", DiskType: "qcow2", Bus: "virtio",
			Path: v.DiskPath, CapacityGB: d.diskGB, AllocGB: d.diskUseGB,
		}}
		fx.vms[d.host] = append(fx.vms[d.host], v)
	}
}

// ─── Storage pools ─────────────────────────────────────────────

func buildStorage(fx *fixtures) {
	fx.storage["hv-demo01.example.lan"] = []*models.StoragePool{
		{Name: "default", HostID: "hv-demo01.example.lan", State: "active", PoolType: "dir", Path: "/var/lib/libvirt/images", CapacityGB: 2048, AllocationGB: 462, AvailableGB: 1586},
		{Name: "isos", HostID: "hv-demo01.example.lan", State: "active", PoolType: "dir", Path: "/var/lib/libvirt/isos", CapacityGB: 512, AllocationGB: 64, AvailableGB: 448},
	}
	fx.storage["hv-demo02.example.lan"] = []*models.StoragePool{
		{Name: "default", HostID: "hv-demo02.example.lan", State: "active", PoolType: "dir", Path: "/var/lib/libvirt/images", CapacityGB: 4096, AllocationGB: 3994, AvailableGB: 102},
	}
	fx.storage["hv-demo03.example.lan"] = []*models.StoragePool{
		{Name: "default", HostID: "hv-demo03.example.lan", State: "active", PoolType: "dir", Path: "/var/lib/libvirt/images", CapacityGB: 8192, AllocationGB: 1161, AvailableGB: 7031},
		{Name: "nvme-fast", HostID: "hv-demo03.example.lan", State: "active", PoolType: "dir", Path: "/mnt/nvme/images", CapacityGB: 2048, AllocationGB: 905, AvailableGB: 1143},
	}
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
		ID:        id,
		Name:      "Demo admin token",
		OwnerID:   adminUserID,
		Kind:      "system",
		Hash:      hex.EncodeToString(sum[:]),
		CreatedAt: now.Add(-14 * 24 * time.Hour),
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
	// Active hourly: web tier
	fx.policies["pol_demo_web_hourly"] = &models.SnapshotPolicy{
		ID: "pol_demo_web_hourly", Name: "Web tier — hourly",
		Enabled:       true,
		VMUUIDs:       []string{"11111111-0001-0000-0000-000000000001", "11111111-0002-0000-0000-000000000001", "11111111-0003-0000-0000-000000000001"},
		EveryMinutes:  60, RetentionKeep: 12,
		CreatedAt:    now.Add(-21 * 24 * time.Hour),
		LastRunAt:    now.Add(-12 * time.Minute),
		LastRunCount: 3,
	}
	// Active daily: db tier
	fx.policies["pol_demo_db_daily"] = &models.SnapshotPolicy{
		ID: "pol_demo_db_daily", Name: "Database — daily",
		Enabled:       true,
		VMUUIDs:       []string{"11111111-0001-0000-0000-000000000003", "11111111-0002-0000-0000-000000000003"},
		EveryMinutes:  1440, RetentionKeep: 7,
		CreatedAt:    now.Add(-45 * 24 * time.Hour),
		LastRunAt:    now.Add(-7 * time.Hour),
		LastRunCount: 2,
	}
	// Paused: old weekly policy that operator disabled
	fx.policies["pol_demo_weekly_paused"] = &models.SnapshotPolicy{
		ID: "pol_demo_weekly_paused", Name: "Weekly full (paused)",
		Enabled:       false,
		VMUUIDs:       []string{"11111111-0001-0000-0000-000000000003"},
		EveryMinutes:  10080, RetentionKeep: 4,
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
					"host.offline":       true,
					"migration.failed":   true,
					"tls.expiring":       true,
					"ca.expiring":        true,
					"vm.crashed":         true,
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

// ─── Migrations (30 days, ~100 records) ────────────────────────

func buildMigrations(fx *fixtures) {
	// Candidate VM+host pairings for synthetic migrations.
	pairs := []struct {
		vmUUID, vmName, src, dst string
	}{
		{"11111111-0001-0000-0000-000000000001", "vm-web01", "hv-demo01.example.lan", "hv-demo02.example.lan"},
		{"11111111-0001-0000-0000-000000000001", "vm-web01", "hv-demo02.example.lan", "hv-demo01.example.lan"},
		{"11111111-0002-0000-0000-000000000001", "vm-web02", "hv-demo02.example.lan", "hv-demo03.example.lan"},
		{"11111111-0002-0000-0000-000000000002", "vm-api02", "hv-demo02.example.lan", "hv-demo03.example.lan"},
		{"11111111-0001-0000-0000-000000000006", "vm-ci01", "hv-demo01.example.lan", "hv-demo02.example.lan"},
		{"11111111-0002-0000-0000-000000000004", "vm-staging", "hv-demo02.example.lan", "hv-demo01.example.lan"},
		{"11111111-0001-0000-0000-000000000002", "vm-api01", "hv-demo01.example.lan", "hv-demo03.example.lan"},
		{"11111111-0003-0000-0000-000000000004", "vm-dev01", "hv-demo03.example.lan", "hv-demo02.example.lan"},
		{"11111111-0002-0000-0000-000000000002", "vm-api02", "hv-demo03.example.lan", "hv-demo02.example.lan"},
		{"11111111-0001-0000-0000-000000000004", "vm-cache01", "hv-demo01.example.lan", "hv-demo02.example.lan"},
	}
	// Produce 100 migrations spread across the last 30 days.
	for i := 0; i < 100; i++ {
		p := pairs[rng.Intn(len(pairs))]
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
			ID:          fmt.Sprintf("%s-%d", p.vmName, startedAt),
			VMName:      p.vmName,
			SourceHost:  p.src,
			DestHost:    p.dst,
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
	hostIDs := []string{"hv-demo01.example.lan", "hv-demo02.example.lan", "hv-demo03.example.lan"}
	for i := 0; i < 500; i++ {
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

// ─── Command log (~50 incl. 3 stranded) ────────────────────────

func buildCommandLog(fx *fixtures) {
	vmList := []struct{ uuid, name, host string }{
		{"11111111-0001-0000-0000-000000000001", "vm-web01", "hv-demo01.example.lan"},
		{"11111111-0002-0000-0000-000000000001", "vm-web02", "hv-demo02.example.lan"},
		{"11111111-0003-0000-0000-000000000001", "vm-web03", "hv-demo03.example.lan"},
		{"11111111-0001-0000-0000-000000000002", "vm-api01", "hv-demo01.example.lan"},
		{"11111111-0002-0000-0000-000000000003", "vm-db02", "hv-demo02.example.lan"},
	}
	actions := []string{"START", "STOP", "RESTART", "SNAPSHOT", "POWER_CYCLE", "EDIT_VM"}
	for i := 0; i < 47; i++ {
		vm := vmList[rng.Intn(len(vmList))]
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
		vm := vmList[i]
		reqID := fmt.Sprintf("req_%s", randHex(16))
		submitted := now.Add(-time.Duration(60+rng.Intn(300)) * time.Minute)
		applied := submitted.Add(4 * time.Hour)
		expires := applied.Add(30 * 24 * time.Hour)
		fx.cmdLog[reqID] = &models.CommandLogEntry{
			RequestID: reqID, Action: "MIGRATE", HostID: vm.host, VMUUID: vm.uuid, VMName: vm.name,
			ActorID: adminUserID, SubmittedAt: submitted, AppliedAt: &applied,
			Outcome: "unknown", StatusCode: 0,
			Message: "server restarted while command was in flight; outcome undetermined - check the target VM state and resolve manually",
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
			ID string `bson:"_id"`
			*models.NotificationConfig `bson:",inline"`
		}{"singleton", fx.notif},
	}})
	cols = append(cols, col{"features", []any{
		struct {
			ID string `bson:"_id"`
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
