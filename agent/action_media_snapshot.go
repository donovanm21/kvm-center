package main

// CHANGE_MEDIA + DELETE_SNAPSHOT actions — extracted from
// commander.go in v5.41.0. Pure move; closes out the per-action
// dispatcher refactor (P2 #1).

import (
	"log"
	"net/http"
	"os/exec"
	"strings"

	"kvm-center/internal/models"
)

// handleChangeMedia performs a live ISO swap on a CDROM target.
// Params:
//
//	media_action = "insert" | "eject"
//	target       = libvirt device name (e.g. "hda", "sdb")
//	path         = pool-resident ISO path (insert only)
func handleChangeMedia(w http.ResponseWriter, req models.CommandRequest, vmTarget string) {
	action := strings.ToLower(strings.TrimSpace(req.Params["media_action"]))
	target := strings.TrimSpace(req.Params["target"])
	if target == "" {
		respond(w, false, "CHANGE_MEDIA requires params.target (CDROM device name)")
		return
	}
	var out []byte
	var err error
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
	msg := strings.TrimSpace(string(out))
	if err != nil {
		log.Printf("command failed: CHANGE_MEDIA: %v — %s", err, msg)
		respond(w, false, msg)
		return
	}
	log.Printf("command ok: CHANGE_MEDIA vm=%s: %s", vmTarget, msg)
	respond(w, true, msg)
}

// handleDeleteSnapshot removes a libvirt domain snapshot. External
// snapshots with backing chains can't be fully deleted while the VM
// is running, so we fall back to --metadata (removes the snapshot
// record but preserves the backing disk files).
func handleDeleteSnapshot(w http.ResponseWriter, req models.CommandRequest, vmTarget string) {
	snapName := req.Params["name"]
	if snapName == "" {
		respond(w, false, "DELETE_SNAPSHOT requires params.name")
		return
	}
	out, err := exec.Command("virsh", "snapshot-delete", vmTarget, snapName).CombinedOutput()
	if err != nil {
		metaOut, metaErr := exec.Command("virsh", "snapshot-delete", vmTarget, snapName, "--metadata").CombinedOutput()
		if metaErr == nil {
			msg := "Snapshot metadata removed (external snapshot — disk backing files preserved): " + strings.TrimSpace(string(metaOut))
			log.Printf("command ok: DELETE_SNAPSHOT (metadata) vm=%s snap=%s", vmTarget, snapName)
			respond(w, true, msg)
			return
		}
		respond(w, false, strings.TrimSpace(string(out)))
		return
	}
	msg := strings.TrimSpace(string(out))
	log.Printf("command ok: DELETE_SNAPSHOT vm=%s snap=%s: %s", vmTarget, snapName, msg)
	respond(w, true, msg)
}
