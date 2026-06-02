package main

// MIGRATE + OFFLINE_MIGRATE actions — extracted from the
// commander.go switch in v5.38.0 (P2 #1, dispatcher refactor part
// 2). Both handlers write their own responses; dispatcher cases
// reduce to a single call + return.
//
// Pure move; no behaviour change. The post-switch envelope in
// commander.go that turns a (out, err) pair into a respond() is
// inlined here for the MIGRATE path so the move is self-contained.

import (
	"log"
	"net/http"
	"strings"

	"kvm-center/internal/models"
)

// handleMigrate runs a synchronous live migration via
// `virsh migrate --live --verbose`. Streams progress frames over
// gRPC for the duration; the SPA reads them off /migrate-status
// for the in-flight progress bar.
func handleMigrate(w http.ResponseWriter, req models.CommandRequest, vmTarget string) {
	dest := req.Params["destination"]
	if dest == "" {
		respond(w, false, "MIGRATE requires params.destination")
		return
	}
	// v5.44.0: operator can flip strict-host-key via Features. Server
	// passes the value through; agent toggles its package-level switch
	// before the SSH-transport paths hit `ssh -o`.
	setSSHStrict(req.Params["strict_host_key"] == "yes")
	// v5.11: forward the server-minted migration grant to the
	// destination agent so the dest validates + records it before
	// libvirtd's TLS handshake starts. Only applies to mTLS
	// migrations; legacy ssh path ignores the param.
	if grant := req.Params["grant"]; grant != "" && req.Params["migration_transport"] == "tls" {
		if err := forwardGrantToDest(dest, grant); err != nil {
			respond(w, false, "destination rejected migration grant: "+err.Error())
			return
		}
	}
	uri := migrationURI(dest, req.Params["migration_transport"])
	// --undefinesource removes the source domain definition once the
	// live migration completes. Without it the UI still lists the
	// "ghost" VM on the source host until the next libvirtd restart,
	// surfacing as a duplicate inventory entry (exactly what bit us
	// on the offline path in v5.4.3).
	args := []string{"migrate", "--live", "--persistent", "--undefinesource", "--verbose"}
	if req.Params["copy_storage"] == "true" {
		args = append(args, "--copy-storage-all")
	}
	args = append(args, vmTarget, uri)
	// v5.32.9: mark the shared MigrationStatus.Active=true so
	// /self-update + bootstrap.sh --update-agents refuse to restart
	// this agent mid-flight.
	setLiveMigrateActive(req.Params["migration_id"], vmTarget, dest, true)
	out, err := runLiveMigrateWithProgress(args, req.Params["migration_id"], vmTarget, dest)
	setLiveMigrateActive(req.Params["migration_id"], vmTarget, dest, false)

	if err != nil {
		msg := strings.TrimSpace(string(out))
		log.Printf("command failed: MIGRATE: %v — %s", err, msg)
		respond(w, false, msg)
		return
	}
	// virsh --verbose's combined output is a flood of `Migration:
	// [ N %]` progress lines. Useful for the agent's own log but
	// noisy as a toast in the SPA — collapse to a clean summary.
	// gRPC frames already carried granular progress.
	log.Printf("command ok: MIGRATE vm=%s (virsh progress lines suppressed in response)", vmTarget)
	respond(w, true, "live migration completed")
}

// handleOfflineMigrate kicks the async offline-migrate worker and
// returns immediately. Operator polls /migrate-status.
func handleOfflineMigrate(w http.ResponseWriter, req models.CommandRequest, vmTarget string) {
	dest := req.Params["destination"]
	if dest == "" {
		respond(w, false, "OFFLINE_MIGRATE requires params.destination")
		return
	}
	setSSHStrict(req.Params["strict_host_key"] == "yes")
	status := getMigStatus()
	if status.Active {
		respond(w, false, "another migration is already in progress: "+status.VMName)
		return
	}
	keepRaw := req.Params["keep_raw"] == "true"
	// v5.11.4: if the server attached a grant (transport=tls),
	// forward it to the destination so the libvirt hook on dest
	// authorises the eventual VM start.
	if grant := req.Params["grant"]; grant != "" && req.Params["migration_transport"] == "tls" {
		if err := forwardGrantToDest(dest, grant); err != nil {
			respond(w, false, "destination rejected migration grant: "+err.Error())
			return
		}
	}
	opts := MTLSOptions{
		TargetPool:    req.Params["target_pool"],
		NewVMName:     req.Params["new_vm_name"],
		RegenerateMAC: req.Params["regenerate_mac"] == "true",
	}
	log.Printf("command: offline-migrate vm=%s → %s keep_raw=%v transport=%s pool=%q rename=%q regen_mac=%v (async)",
		vmTarget, dest, keepRaw, req.Params["migration_transport"], opts.TargetPool, opts.NewVMName, opts.RegenerateMAC)
	startOfflineMigrate(vmTarget, dest, req.Params["migration_id"], keepRaw, req.Params["migration_transport"], opts)
	respond(w, true, "offline migration started — poll /migrate-status for progress")
}
