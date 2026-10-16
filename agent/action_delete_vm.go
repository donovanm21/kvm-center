package main

// DELETE_VM action. Self-handles the response.

import (
	"log"
	"net/http"
	"os/exec"
	"strings"

	"kvm-center/internal/models"
)

// handleDeleteVM tears down a VM in stages: force-off (if still
// running), then undefine with NVRAM + managed-save + snapshot metadata
// removal. Disk images and attached media are always retained. The agent
// has no authoritative record of exclusive storage ownership: neither a
// domain XML source, pool membership, nor a filename establishes that a
// disk is not shared, imported, or used as another image's backing file.
func handleDeleteVM(w http.ResponseWriter, req models.CommandRequest, vmTarget string) {
	// Fail before any mutation, including power-off. Silently retaining disks
	// after a destructive request would falsely report that cleanup succeeded.
	// Do not reintroduce automatic removal without a trusted ownership and
	// dependency contract that also accounts for other hosts and snapshots.
	switch req.Params["delete_disks"] {
	case "", "false":
		// Default and explicitly requested domain-only deletion.
	case "true":
		respond(w, false, "automatic disk deletion is unavailable because exclusive storage ownership cannot be verified; no changes were made. Retry with delete_disks=false to remove the VM definition and state while retaining disk files")
		return
	default:
		respond(w, false, "invalid delete_disks value; use delete_disks=false to retain disk files. No changes were made")
		return
	}
	if out, derr := exec.Command("virsh", "destroy", vmTarget).CombinedOutput(); derr != nil {
		// "domain is not running" is fine; anything else aborts.
		msg := strings.ToLower(string(out))
		if !strings.Contains(msg, "not running") && !strings.Contains(msg, "is not active") {
			respond(w, false, "could not force-off VM: "+strings.TrimSpace(string(out)))
			return
		}
	}
	// Never pass --remove-all-storage (or a caller-provided storage list).
	// libvirt's storage removal would bypass the ownership guard above.
	out, err := exec.Command("virsh", "undefine", vmTarget,
		"--nvram", "--managed-save", "--snapshots-metadata").CombinedOutput()

	msg := strings.TrimSpace(string(out))
	if err != nil {
		log.Printf("command failed: DELETE_VM: %v — %s", err, msg)
		respond(w, false, msg)
		return
	}
	log.Printf("command ok: DELETE_VM vm=%s: %s", vmTarget, msg)
	respond(w, true, strings.TrimSpace(msg+" Disk files retained."))
}
