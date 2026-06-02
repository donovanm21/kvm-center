package main

// DELETE_VM action — extracted from commander.go in v5.38.0
// (P2 #1 dispatcher refactor part 2). Pure move, no behaviour
// change. Self-handles the response.

import (
	"log"
	"net/http"
	"os/exec"
	"strings"

	"kvm-center/internal/models"
)

// handleDeleteVM tears down a VM in stages: force-off (if still
// running), undefine with NVRAM + managed-save + snapshot metadata
// removal, optional disk file removal. The UI gates this behind
// its own multi-step confirm; delete_disks defaults to false so
// an accidental DELETE doesn't lose data.
func handleDeleteVM(w http.ResponseWriter, req models.CommandRequest, vmTarget string) {
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
	out, err := exec.Command("virsh", undefArgs...).CombinedOutput()
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

	msg := strings.TrimSpace(string(out))
	if err != nil {
		log.Printf("command failed: DELETE_VM: %v — %s", err, msg)
		respond(w, false, msg)
		return
	}
	log.Printf("command ok: DELETE_VM vm=%s: %s", vmTarget, msg)
	respond(w, true, msg)
}
