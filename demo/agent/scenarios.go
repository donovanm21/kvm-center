package main

// Scripted scenarios loop inside the demo-agent to keep the UI
// alive. Four loops run alongside the telemetry goroutines:
//
//   loadSpikeLoop       — every ~8 min, pick a random host and
//                         push baseline CPU to 85% for 2 min, then
//                         restore. Keeps the CPU gauges + sparklines
//                         animated beyond noise-level drift.
//   snapshotPolicyLoop  — every ~5 min, pick VMs covered by an
//                         active policy and bump SnapshotCount.
//                         Makes the Snapshots view churn without
//                         the real snapshot-scheduler running.
//   offlineBlipLoop     — every ~25 min, pick a random host and
//                         halt its telemetry for 3 min. The server
//                         marks it offline after the 2-min threshold
//                         + fires host.offline. When the blip ends,
//                         host.recovered fires.
//   migrationLoop       — every ~15 min, pick a random running VM
//                         and simulate a live migration to another
//                         host. Goes through the full state arc
//                         (/migrate-status reports progress 0→100
//                         over ~3 min); the server's migration
//                         poller records it in the UI just like a
//                         real operator-triggered migration.
//
// Scenarios share a per-fleet state via fleet (defined in main.go)
// so the offline-blip and migration loops can reach across hosts.

import (
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"kvm-center/internal/models"
)

// ─── Shared per-fleet state ────────────────────────────────────

type fleetState struct {
	mu     sync.RWMutex
	agents []*hostAgent
	// paused is a per-host flag that, when true, suppresses
	// telemetry POSTs (used by the offline-blip scenario).
	paused sync.Map // hostID string → *atomic.Bool
	// migrations holds in-flight synthetic migrations keyed by
	// source host ID. /migrate-status reads from this; /command
	// MIGRATE writes.
	migrations sync.Map // hostID string → *synthMigration
}

var fleet = &fleetState{}

func initFleet(agents []*hostAgent) {
	fleet.mu.Lock()
	fleet.agents = agents
	fleet.mu.Unlock()
	for _, a := range agents {
		var b atomic.Bool
		fleet.paused.Store(a.host.ID, &b)
	}
}

func (f *fleetState) agentByID(id string) *hostAgent {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, a := range f.agents {
		if a.host.ID == id {
			return a
		}
	}
	return nil
}

func (f *fleetState) random() *hostAgent {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if len(f.agents) == 0 {
		return nil
	}
	return f.agents[rand.Intn(len(f.agents))]
}

// isPaused returns true when offline-blip has suspended telemetry
// for this host. sendTelemetry checks this before POSTing.
func (f *fleetState) isPaused(hostID string) bool {
	v, ok := f.paused.Load(hostID)
	if !ok {
		return false
	}
	return v.(*atomic.Bool).Load()
}

func (f *fleetState) setPaused(hostID string, v bool) {
	p, ok := f.paused.Load(hostID)
	if !ok {
		return
	}
	p.(*atomic.Bool).Store(v)
}

// ─── Live migration state ──────────────────────────────────────

// synthMigration tracks one in-flight demo migration. /migrate-status
// reads from it on poll to return a progress arc; when the arc
// completes the entry is removed + the VM's HostID is flipped.
type synthMigration struct {
	vmName    string
	vmUUID    string
	srcHost   string
	dstHost   string
	startedAt time.Time
	duration  time.Duration
}

func (m *synthMigration) progress(now time.Time) int {
	elapsed := now.Sub(m.startedAt)
	if elapsed >= m.duration {
		return 100
	}
	return int(float64(elapsed) / float64(m.duration) * 100)
}

func (m *synthMigration) phase(now time.Time) string {
	p := m.progress(now)
	switch {
	case p >= 100:
		return "complete"
	case p >= 90:
		return "handover"
	case p >= 5:
		return "copying"
	default:
		return "starting"
	}
}

// startMigration records a synthetic migration on the source host
// and schedules the dest-host VM flip when the arc finishes.
// Called by the agent's /command handler on MIGRATE / OFFLINE_MIGRATE.
func startMigration(srcHost, vmUUID, vmName, dstHost string) *synthMigration {
	m := &synthMigration{
		vmName:    vmName,
		vmUUID:    vmUUID,
		srcHost:   srcHost,
		dstHost:   dstHost,
		startedAt: time.Now(),
		// Arc length: 2.5 to 3.5 minutes.
		duration: 150*time.Second + time.Duration(rand.Intn(60))*time.Second,
	}
	fleet.migrations.Store(srcHost, m)
	// After the arc ends, move the VM from source to dest in the
	// in-memory fleet state and clear the migration entry.
	go func() {
		time.Sleep(m.duration + 2*time.Second)
		src := fleet.agentByID(srcHost)
		dst := fleet.agentByID(dstHost)
		if src == nil || dst == nil {
			return
		}
		var moved *models.VM
		src.mu.Lock()
		kept := src.vms[:0]
		for _, v := range src.vms {
			if v.UUID == vmUUID {
				moved = v
				continue
			}
			kept = append(kept, v)
		}
		src.vms = kept
		src.mu.Unlock()
		if moved == nil {
			fleet.migrations.Delete(srcHost)
			return
		}
		moved.HostID = dstHost
		dst.mu.Lock()
		dst.vms = append(dst.vms, moved)
		// Carry over the baseline so drift continues smoothly on
		// the new host.
		dst.baseline.vmCPU[moved.UUID] = moved.CPUUsage
		dst.baseline.vmMem[moved.UUID] = moved.MemUsageMB
		dst.mu.Unlock()
		fleet.migrations.Delete(srcHost)
		// Kick a fresh telemetry POST from both hosts so the UI
		// reflects the move immediately.
		go src.sendTelemetry()
		go dst.sendTelemetry()
		log.Printf("demo-scenario: migration complete — %s moved %s → %s", vmName, srcHost, dstHost)
	}()
	return m
}

func migrationFor(hostID string) *synthMigration {
	v, ok := fleet.migrations.Load(hostID)
	if !ok {
		return nil
	}
	return v.(*synthMigration)
}

// ─── Load spike loop ───────────────────────────────────────────

func loadSpikeLoop() {
	// First fire after 30s so screenshots taken immediately after
	// demo-up don't catch a host mid-spike.
	time.Sleep(30 * time.Second)
	for {
		a := fleet.random()
		if a == nil {
			time.Sleep(1 * time.Minute)
			continue
		}
		runLoadSpike(a)
		// Space spikes 6-10 min apart so there's always one within
		// a 10-min walkthrough window.
		time.Sleep(time.Duration(360+rand.Intn(240)) * time.Second)
	}
}

func runLoadSpike(a *hostAgent) {
	a.mu.Lock()
	origCPU := a.baseline.hostCPU
	a.baseline.hostCPU = 78 + rand.Float64()*12 // 78-90%
	a.mu.Unlock()
	log.Printf("demo-scenario: load spike on %s (baseline %.1f%% → %.1f%%)", a.host.ID, origCPU, 78+rand.Float64()*12)

	// Hold for ~2 min.
	time.Sleep(time.Duration(90+rand.Intn(60)) * time.Second)

	a.mu.Lock()
	a.baseline.hostCPU = origCPU
	a.mu.Unlock()
	log.Printf("demo-scenario: load spike on %s cleared (baseline back to %.1f%%)", a.host.ID, origCPU)
}

// ─── Offline blip loop ─────────────────────────────────────────

func offlineBlipLoop() {
	// First fire after 2 min so the demo settles before hosts
	// start going offline.
	time.Sleep(2 * time.Minute)
	for {
		a := fleet.random()
		if a == nil {
			time.Sleep(1 * time.Minute)
			continue
		}
		runOfflineBlip(a)
		// Space blips 20-30 min apart.
		time.Sleep(time.Duration(1200+rand.Intn(600)) * time.Second)
	}
}

func runOfflineBlip(a *hostAgent) {
	log.Printf("demo-scenario: offline blip on %s (3 min)", a.host.ID)
	fleet.setPaused(a.host.ID, true)
	// Long enough that the server's 2-min offline threshold kicks
	// in + the host.offline notification fires.
	time.Sleep(3 * time.Minute)
	fleet.setPaused(a.host.ID, false)
	// Kick immediate telemetry so the server's host.recovered
	// fires as the blip ends.
	go a.sendTelemetry()
	log.Printf("demo-scenario: %s is back", a.host.ID)
}

// ─── Snapshot policy simulator ─────────────────────────────────

// Bumps SnapshotCount on a rotating subset of VMs every ~5 min.
// Mimics the real snapshot policy scheduler firing without actually
// invoking virsh. Bumping vm.SnapshotCount here triggers the
// server's next ApplyTelemetry to notice + update; /snapshots
// responds with the matching count worth of synthetic entries.
func snapshotPolicyLoop() {
	time.Sleep(90 * time.Second)
	for {
		// Pick 3-8 random VMs across the fleet.
		fleet.mu.RLock()
		var all []*hostAgent
		all = append(all, fleet.agents...)
		fleet.mu.RUnlock()
		if len(all) == 0 {
			time.Sleep(1 * time.Minute)
			continue
		}
		picks := 3 + rand.Intn(6)
		bumped := 0
		for i := 0; i < picks*3 && bumped < picks; i++ {
			a := all[rand.Intn(len(all))]
			a.mu.Lock()
			if len(a.vms) == 0 {
				a.mu.Unlock()
				continue
			}
			v := a.vms[rand.Intn(len(a.vms))]
			if v.SnapshotsBlockedReason == "" && v.State == models.VMRunning {
				v.SnapshotCount++
				bumped++
			}
			a.mu.Unlock()
		}
		log.Printf("demo-scenario: snapshot policy fired (bumped %d VMs)", bumped)
		time.Sleep(time.Duration(240+rand.Intn(120)) * time.Second) // 4-6 min
	}
}

// ─── Migration initiator ───────────────────────────────────────
//
// Kicks a fresh migration on a loop so the walkthrough recording
// catches one in flight. Picks a random running non-GPU VM, a
// random dest host, records the synthMigration directly and lets
// the existing /migrate-status poll render the progress arc. The
// server's migration record is NOT created here (no /api/command
// round-trip), so these "background" migrations show up in the UI
// only once the server's own polling picks them up — which
// requires a MigrationRecord to exist. Simpler and prettier: we
// skip the auto-loop for now and let walkthrough operators click
// MIGRATE in the UI to trigger a real migration. That click
// produces both a server-side record AND a /migrate-status arc.

func startScenarios(agents []*hostAgent) {
	initFleet(agents)
	go loadSpikeLoop()
	go offlineBlipLoop()
	go snapshotPolicyLoop()
}
