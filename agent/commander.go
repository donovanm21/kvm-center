package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
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
	mux.HandleFunc("/snapshots/", handleSnapshotList)
	mux.HandleFunc("/migrate-status", handleMigrateStatus)
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
		uri := fmt.Sprintf("qemu+ssh://kvm-migrator@%s/system?keyfile=/var/lib/kvm-migrator/.ssh/id_ed25519&no_verify=1", dest)
		args := []string{"migrate", "--live", "--persistent", "--verbose"}
		if req.Params["copy_storage"] == "true" {
			args = append(args, "--copy-storage-all")
		}
		args = append(args, vmTarget, uri)
		out, err = exec.Command("virsh", args...).CombinedOutput()
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
		log.Printf("command: offline-migrate vm=%s → %s keep_raw=%v (async)", vmTarget, dest, keepRaw)
		startOfflineMigrate(vmTarget, dest, keepRaw)
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
