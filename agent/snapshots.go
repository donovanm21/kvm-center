package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strings"

	"kvm-center/internal/models"
)

// snapLineRE matches one row of `virsh snapshot-list <vm> --parent` output.
// Columns: Name  Creation Time (YYYY-MM-DD HH:MM:SS ±ZZZZ)  State  [Parent]
var snapLineRE = regexp.MustCompile(
	`^\s*(\S+)\s+(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}\s+[+\-]\d{4})\s+(\S+)(?:\s+(\S+))?\s*$`,
)

// handleSnapshotList serves GET /snapshots/{vmName} with the VM's snapshot list.
func handleSnapshotList(w http.ResponseWriter, r *http.Request) {
	vmName := strings.TrimPrefix(r.URL.Path, "/snapshots/")
	if vmName == "" {
		http.Error(w, "vm name required", http.StatusBadRequest)
		return
	}
	snaps := listSnapshots(vmName)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"snapshots": snaps})
}

// listSnapshots returns all snapshots for the given VM, including the current-snapshot mark.
func listSnapshots(vmName string) []models.Snapshot {
	out, err := exec.Command("virsh", "snapshot-list", vmName, "--parent").CombinedOutput()
	if err != nil {
		log.Printf("snapshots: list %s: %v", vmName, err)
		return []models.Snapshot{}
	}

	current := currentSnapshotName(vmName)

	var snaps []models.Snapshot
	for _, line := range strings.Split(string(out), "\n") {
		m := snapLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		snaps = append(snaps, models.Snapshot{
			Name:         m[1],
			Domain:       vmName,
			CreationTime: m[2],
			State:        m[3],
			Parent:       m[4],
			IsCurrent:    m[1] == current,
		})
	}
	if snaps == nil {
		snaps = []models.Snapshot{}
	}
	return snaps
}

// currentSnapshotName returns the name of the VM's current snapshot, or "" if none.
func currentSnapshotName(vmName string) string {
	out, err := exec.Command("virsh", "snapshot-current", vmName, "--name").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
