package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// StartCommandServer listens for POST /command from the control plane.
func StartCommandServer(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/command", handleCommand)
	mux.HandleFunc("/console/", handleConsolePage)
	mux.HandleFunc("/ws/", handleConsoleWS)
	mux.HandleFunc("/screenshot/", handleVMScreenshot)
	mux.HandleFunc("/snapshots/", handleSnapshotList)
	mux.HandleFunc("/migrate-status", handleMigrateStatus)
	// v5.11: destination-side endpoint that validates + caches an
	// incoming migration grant right before libvirtd handshakes with
	// the source over qemu+tls.
	mux.HandleFunc("/prepare-grant", handlePrepareGrant)
	// v5.11.1: consulted by /etc/libvirt/hooks/qemu to enforce that a
	// migration the dest is about to accept was actually authorised.
	mux.HandleFunc("/grant-active", handleGrantActive)
	mux.HandleFunc("/cancel-migrate", handleCancelMigrate)
	mux.HandleFunc("/storage-browse", handleStorageBrowse)
	mux.HandleFunc("/storage-upload", handleStorageUpload)
	mux.HandleFunc("/storage-file", handleStorageDelete)
	mux.HandleFunc("/vm-create-info", handleVMCreateInfo)
	mux.HandleFunc("/vm-create", handleVMCreate)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	log.Printf("agent command server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("commander: %v", err)
	}
}

func handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req models.CommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond(w, false, "invalid JSON: "+err.Error())
		return
	}

	vmTarget := req.VMName
	if vmTarget == "" {
		vmTarget = req.VMUUID
	}
	if vmTarget == "" {
		respond(w, false, "vm_name or vm_uuid required")
		return
	}

	log.Printf("command: action=%s vm=%s", req.Action, vmTarget)

	var (
		out []byte
		err error
	)

	switch strings.ToUpper(req.Action) {
	case "START":
		out, err = exec.Command("virsh", "start", vmTarget).CombinedOutput()
	case "STOP":
		// Prefer graceful shutdown; fall back to destroy after 30s (handled by the caller).
		out, err = exec.Command("virsh", "shutdown", vmTarget).CombinedOutput()
	case "FORCESTOP":
		out, err = exec.Command("virsh", "destroy", vmTarget).CombinedOutput()
	case "RESTART":
		out, err = exec.Command("virsh", "reboot", vmTarget).CombinedOutput()
	case "SNAPSHOT":
		snapName := req.Params["name"]
		if snapName == "" {
			snapName = fmt.Sprintf("snap-%d", timeNow())
		}
		out, err = exec.Command("virsh", "snapshot-create-as", vmTarget, snapName).CombinedOutput()
	case "REVERT_SNAPSHOT":
		snapName := req.Params["name"]
		if snapName == "" {
			respond(w, false, "REVERT_SNAPSHOT requires params.name")
			return
		}
		out, err = exec.Command("virsh", "snapshot-revert", vmTarget, snapName, "--force").CombinedOutput()
	case "EDIT_VM":
		// Live hardware edit — vcpus + memory. Params:
		//   vcpus   = target integer (optional)
		//   mem_mb  = target integer MB (optional; balloon)
		// --config persists; --live attempts hot-apply. Errors from
		// virsh (e.g. "cannot change maxMemory live") bubble back up.
		var msgs []string
		if v := strings.TrimSpace(req.Params["vcpus"]); v != "" {
			o, e := exec.Command("virsh", "setvcpus", vmTarget, v, "--config", "--live").CombinedOutput()
			if e != nil {
				// Live may be rejected; retry --config only so the
				// change at least lands on next boot.
				o2, e2 := exec.Command("virsh", "setvcpus", vmTarget, v, "--config").CombinedOutput()
				if e2 != nil {
					respond(w, false, fmt.Sprintf("setvcpus failed: %s / %s", strings.TrimSpace(string(o)), strings.TrimSpace(string(o2))))
					return
				}
				msgs = append(msgs, "vcpus="+v+" (config-only, reboot to apply)")
			} else {
				msgs = append(msgs, "vcpus="+v+" (live+config)")
			}
		}
		if m := strings.TrimSpace(req.Params["mem_mb"]); m != "" {
			// virsh setmem takes KiB. Parse MiB → KiB.
			mbN, convErr := strconv.Atoi(m)
			if convErr != nil {
				respond(w, false, "mem_mb must be an integer: "+convErr.Error())
				return
			}
			kib := fmt.Sprintf("%d", mbN*1024)
			o, e := exec.Command("virsh", "setmem", vmTarget, kib, "--config", "--live").CombinedOutput()
			if e != nil {
				o2, e2 := exec.Command("virsh", "setmem", vmTarget, kib, "--config").CombinedOutput()
				if e2 != nil {
					respond(w, false, fmt.Sprintf("setmem failed: %s / %s", strings.TrimSpace(string(o)), strings.TrimSpace(string(o2))))
					return
				}
				msgs = append(msgs, "mem="+m+"MB (config-only, reboot to apply)")
			} else {
				msgs = append(msgs, "mem="+m+"MB (live+config)")
			}
		}
		if len(msgs) == 0 {
			respond(w, false, "EDIT_VM requires at least one of params.vcpus, params.mem_mb")
			return
		}
		respond(w, true, "EDIT_VM: "+strings.Join(msgs, ", "))
		return
	case "CHANGE_MEDIA":
		// Live ISO swap on a CDROM target. Params:
		//   media_action = "insert" | "eject"
		//   target       = libvirt device name (e.g. "hda", "sdb")
		//   path         = pool-resident ISO path (insert only)
		action := strings.ToLower(strings.TrimSpace(req.Params["media_action"]))
		target := strings.TrimSpace(req.Params["target"])
		if target == "" {
			respond(w, false, "CHANGE_MEDIA requires params.target (CDROM device name)")
			return
		}
		switch action {
		case "eject":
			out, err = exec.Command("virsh", "change-media", vmTarget, target, "--eject", "--live", "--config").CombinedOutput()
		case "insert":
			path := strings.TrimSpace(req.Params["path"])
			if path == "" {
				respond(w, false, "CHANGE_MEDIA insert requires params.path")
				return
			}
			out, err = exec.Command("virsh", "change-media", vmTarget, target, path, "--insert", "--live", "--config").CombinedOutput()
		default:
			respond(w, false, "CHANGE_MEDIA requires params.media_action=insert|eject")
			return
		}
	case "DELETE_SNAPSHOT":
		snapName := req.Params["name"]
		if snapName == "" {
			respond(w, false, "DELETE_SNAPSHOT requires params.name")
			return
		}
		out, err = exec.Command("virsh", "snapshot-delete", vmTarget, snapName).CombinedOutput()
		if err != nil {
			// External snapshots with backing chains can't be fully deleted while
			// the VM is running. Fall back to --metadata (removes the snapshot
			// record but preserves the backing disk files).
			metaOut, metaErr := exec.Command("virsh", "snapshot-delete", vmTarget, snapName, "--metadata").CombinedOutput()
			if metaErr == nil {
				msg := "Snapshot metadata removed (external snapshot — disk backing files preserved): " + strings.TrimSpace(string(metaOut))
				log.Printf("command ok: DELETE_SNAPSHOT (metadata) vm=%s snap=%s", vmTarget, snapName)
				respond(w, true, msg)
				return
			}
		}
	case "MIGRATE":
		dest := req.Params["destination"]
		if dest == "" {
			respond(w, false, "MIGRATE requires params.destination")
			return
		}
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
		out, err = exec.Command("virsh", args...).CombinedOutput()
	case "DELETE_VM":
		// Multi-stage destruction: force-off if still running, undefine with
		// NVRAM + managed save + snapshot metadata, optionally remove the
		// backing disks reported by dumpxml. The UI gates this behind its
		// own multi-step confirmation; delete_disks defaults to false so
		// an accidental DELETE doesn't lose data.
		deleteDisks := req.Params["delete_disks"] == "true"
		if out, derr := exec.Command("virsh", "destroy", vmTarget).CombinedOutput(); derr != nil {
			// "domain is not running" is fine; anything else aborts.
			msg := strings.ToLower(string(out))
			if !strings.Contains(msg, "not running") && !strings.Contains(msg, "is not active") {
				respond(w, false, "could not force-off VM: "+strings.TrimSpace(string(out)))
				return
			}
		}
		diskPaths := collectVMDiskPaths(vmTarget)
		undefArgs := []string{"undefine", vmTarget, "--nvram", "--managed-save", "--snapshots-metadata"}
		if deleteDisks {
			// --remove-all-storage only works when the disks live in a libvirt
			// storage pool. If that fails we retry without and unlink files
			// ourselves below.
			undefArgs = append(undefArgs, "--remove-all-storage")
		}
		out, err = exec.Command("virsh", undefArgs...).CombinedOutput()
		if err != nil && deleteDisks && len(diskPaths) > 0 {
			if out2, err2 := exec.Command("virsh", "undefine", vmTarget,
				"--nvram", "--managed-save", "--snapshots-metadata").CombinedOutput(); err2 == nil {
				out, err = out2, nil
			}
		}
		if err == nil && deleteDisks {
			for _, p := range diskPaths {
				if rerr := removeDiskFile(p); rerr != nil {
					log.Printf("delete_vm: could not remove %s: %v", p, rerr)
				}
			}
		}
		if err == nil && !deleteDisks && len(diskPaths) > 0 {
			log.Printf("delete_vm: kept %d disk file(s) — delete_disks=false", len(diskPaths))
		}
	case "OFFLINE_MIGRATE":
		dest := req.Params["destination"]
		if dest == "" {
			respond(w, false, "OFFLINE_MIGRATE requires params.destination")
			return
		}
		status := getMigStatus()
		if status.Active {
			respond(w, false, "another migration is already in progress: "+status.VMName)
			return
		}
		keepRaw := req.Params["keep_raw"] == "true"
		// v5.11.4: if the server attached a grant (transport=tls),
		// forward it to the destination so the libvirt hook on dest
		// authorises the eventual VM start. v5.11.5 will switch the
		// data transfer itself to qemu+tls vol-upload; v5.11.4 still
		// uses scp but ties offline into the same grant path.
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
		startOfflineMigrate(vmTarget, dest, keepRaw, req.Params["migration_transport"], opts)
		respond(w, true, "offline migration started — poll /migrate-status for progress")
		return
	default:
		respond(w, false, "unknown action: "+req.Action)
		return
	}

	msg := strings.TrimSpace(string(out))
	if err != nil {
		log.Printf("command failed: %s: %v — %s", req.Action, err, msg)
		respond(w, false, msg)
		return
	}
	log.Printf("command ok: %s vm=%s: %s", req.Action, vmTarget, msg)
	respond(w, true, msg)
}

// migrationURI builds the libvirt URI for a live migration. Transport
// selector (v5.10.1):
//
//	"tls" — qemu+tls://<dest>/system. Requires libvirtd on both ends
//	        to have been configured with the cluster CA + a cert (see
//	        install-agent.sh --enroll-token). This is the default once
//	        both hypervisors are enrolled.
//	""/"ssh" — legacy qemu+ssh:// via the shared kvm-migrator keypair.
//	        Retained as a fallback for mixed fleets during v5.10.x
//	        rollout. Will be removed in v5.11.
func migrationURI(dest, transport string) string {
	if transport == "tls" {
		// verify=1 (default) — libvirtd enforces chain validation against
		// /etc/pki/CA/cacert.pem on both sides. tls_port=16514 is libvirt's
		// default TLS listener port.
		return fmt.Sprintf("qemu+tls://%s/system", dest)
	}
	return fmt.Sprintf("qemu+ssh://kvm-migrator@%s/system?keyfile=/var/lib/kvm-migrator/.ssh/id_ed25519&no_verify=1", dest)
}

// collectVMDiskPaths scrapes the domain XML for the disk source paths so
// we can unlink them after undefine when the disks weren't part of a
// libvirt storage pool (very common for qcow2 images under
// /var/lib/libvirt/images/). Returns an empty slice on any parse failure
// — the caller already guarded the undefine itself.
func collectVMDiskPaths(vmTarget string) []string {
	xml, err := exec.Command("virsh", "dumpxml", vmTarget).CombinedOutput()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(xml), "\n") {
		line = strings.TrimSpace(line)
		// <source file='/var/lib/libvirt/images/vm.qcow2'/>
		if !strings.HasPrefix(line, "<source ") {
			continue
		}
		const key = "file='"
		i := strings.Index(line, key)
		if i < 0 {
			continue
		}
		rest := line[i+len(key):]
		j := strings.IndexByte(rest, '\'')
		if j <= 0 {
			continue
		}
		paths = append(paths, rest[:j])
	}
	return paths
}

func removeDiskFile(path string) error {
	// Only operate on files under the common libvirt image directories.
	// Refuse anything else so a compromised control plane can't use this
	// as an arbitrary-delete primitive.
	safe := false
	for _, prefix := range []string{"/var/lib/libvirt/images/", "/var/lib/libvirt/boot/", "/tmp/"} {
		if strings.HasPrefix(path, prefix) {
			safe = true
			break
		}
	}
	if !safe {
		return fmt.Errorf("refusing to remove %q: outside allowed libvirt image paths", path)
	}
	return os.Remove(path)
}

func respond(w http.ResponseWriter, success bool, message string) {
	w.Header().Set("Content-Type", "application/json")
	if !success {
		w.WriteHeader(http.StatusInternalServerError)
	}
	json.NewEncoder(w).Encode(models.CommandResponse{
		Success: success,
		Message: message,
	})
}

func timeNow() int64 {
	return time.Now().Unix()
}
