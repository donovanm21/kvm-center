package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	select {
	case <-serverContext().Done():
		return
	case <-time.After(60 * time.Second):
	}

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
		// Within the 90-min window but ask the source agent whether it
		// actually has work in flight. If the agent says active=false,
		// the migration was killed (most commonly by an agent restart
		// during a deploy — v5.29.0 hit this). Mark failed so the UI
		// stops showing a stuck progress bar.
		if agentReportsNoActiveMigration(store, rec.SourceHost) {
			finalizeMigration(store, rec, "failed",
				"Source agent reports no active migration — transfer was interrupted (agent restart?). Disk/record cleanup may be needed.")
			log.Printf("migrations: reconciled %s as failed (agent idle, vm=%s src=%s)",
				rec.ID, rec.VMName, rec.SourceHost)
			fixed++
			continue
		}
		// Agent says active=true OR agent unreachable — leave as-is.
		// The next boot of the reconciler (or the sync HTTP completion)
		// will finish the job.
	}
	if fixed > 0 {
		log.Printf("migrations: reconciler resolved %d stranded record(s) at boot", fixed)
	}
}

// agentReportsNoActiveMigration hits the source agent's /migrate-status
// endpoint. Returns true only when the response clearly says no
// migration is in flight — a network error or unexpected response
// returns false so we don't fail a live migration on a flaky probe.
func agentReportsNoActiveMigration(store *Store, sourceHost string) bool {
	if sourceHost == "" {
		return false
	}
	addr := store.GetHostAgentAddr(sourceHost)
	if addr == "" {
		return false
	}
	client := store.newAgentClient(sourceHost, 3*time.Second)
	url := fmt.Sprintf("http://%s/migrate-status", addr)
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return !body.Active
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
