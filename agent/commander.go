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
	case "MIGRATE":
		dest := req.Params["destination"]
		if dest == "" {
			respond(w, false, "MIGRATE requires params.destination")
			return
		}
		uri := fmt.Sprintf("qemu+ssh://%s/system", dest)
		out, err = exec.Command(
			"virsh", "migrate", "--live", "--persistent",
			vmTarget, uri,
		).CombinedOutput()
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
