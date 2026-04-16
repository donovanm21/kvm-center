package main

import (
	"log"
	"strings"
	"time"
)

// reconcileStrandedMigrations runs once at boot to clean up migration
// records left in "running" status by a server restart mid-flight.
//
// Scenario: the server dispatches MIGRATE to an agent, the agent runs
// `virsh migrate` synchronously (minutes), and in between the server
// process is replaced (deploy, crash, etc.). The agent eventually
// returns 200, but that HTTP response goes to a closed socket —
// finalizeMigration never runs, and the Mongo record stays
// Status="running" forever. The UI shows it flashing indefinitely.
//
// Reconciler heuristic:
//   - If the VM is currently on the destination host (per the fresh
//     telemetry we already have cached by the time this runs),
//     mark the record complete.
//   - Otherwise, if the record's started_at is older than the
//     in-flight migration timeout (90 minutes), mark it failed.
//   - Anything newer is left running — either the agent is still
//     mid-migration or a subsequent telemetry tick will provide the
//     answer.
func reconcileStrandedMigrations(store *Store) {
	// Wait a minute so hypervisor telemetry has a chance to arrive —
	// GetVMs returns empty until agents have checked in. Without this
	// delay we'd wrongly mark "complete" records as "failed" simply
	// because the destination host hadn't reported its VM list yet.
	time.Sleep(60 * time.Second)

	const inflightTimeout = 90 * time.Minute
	now := time.Now()
	fixed := 0
	for _, rec := range store.GetMigrations() {
		if rec == nil || rec.Status != "running" {
			continue
		}
		age := now.Sub(time.Unix(rec.StartedAt, 0))

		if vmIsOnHost(store, rec.VMName, rec.DestHost) {
			finalizeMigration(store, rec, "complete",
				"Reconciled at server boot — VM confirmed on destination host")
			log.Printf("migrations: reconciled %s as complete (vm=%s on %s)",
				rec.ID, rec.VMName, rec.DestHost)
			fixed++
			continue
		}

		if age > inflightTimeout {
			finalizeMigration(store, rec, "failed",
				"Stranded by server restart — exceeded in-flight timeout while the server was down")
			log.Printf("migrations: reconciled %s as failed (age=%s, vm=%s not on %s)",
				rec.ID, age.Truncate(time.Second), rec.VMName, rec.DestHost)
			fixed++
			continue
		}
		// Still within the in-flight window; a live agent may still
		// be working. Leave as-is.
	}
	if fixed > 0 {
		log.Printf("migrations: reconciler resolved %d stranded record(s) at boot", fixed)
	}
}

// vmIsOnHost checks the in-memory telemetry cache for the given VM on
// the given host. Name compare is case-insensitive and tolerant of
// FQDN vs short host IDs.
func vmIsOnHost(store *Store, vmName, hostID string) bool {
	if vmName == "" || hostID == "" {
		return false
	}
	wantHost := strings.ToLower(hostID)
	wantShort := strings.SplitN(wantHost, ".", 2)[0]
	wantVM := strings.ToLower(vmName)

	for _, h := range store.GetHosts() {
		if h == nil {
			continue
		}
		hid := strings.ToLower(h.ID)
		if hid != wantHost && strings.SplitN(hid, ".", 2)[0] != wantShort {
			continue
		}
		for _, vm := range store.GetVMs(h.ID) {
			if vm != nil && strings.ToLower(vm.Name) == wantVM {
				return true
			}
		}
	}
	return false
}
