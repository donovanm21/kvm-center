package main

// collectPostEditFields re-reads the domain XML after EDIT_VM so the
// agent can tell the server exactly what changed. Returning only the
// keys that appeared in the edit request keeps the response tight;
// the server uses this to refresh its in-memory VM record
// immediately instead of waiting for the 30 s telemetry tick.

import (
	"encoding/xml"
	"os/exec"
	"strconv"
	"strings"
)

// postEditDomain subset of dumpxml used only for the readback.
type postEditDomain struct {
	Memory        int64 `xml:"memory"`
	CurrentMemory int64 `xml:"currentMemory"`
	Vcpu          int   `xml:"vcpu"`
	OS            struct {
		Boot struct {
			Dev string `xml:"dev,attr"`
		} `xml:"boot"`
	} `xml:"os"`
	CPU struct {
		Mode string `xml:"mode,attr"`
	} `xml:"cpu"`
	Devices struct {
		Video struct {
			Model struct {
				Type string `xml:"type,attr"`
			} `xml:"model"`
		} `xml:"video"`
	} `xml:"devices"`
}

func collectPostEditFields(vmTarget string, params map[string]string) map[string]any {
	// --inactive reads the persisted config, not the live snapshot.
	// That matches what the operator will see on next boot, and
	// crucially aligns with the SPA's mem_total_mb field (which
	// maps to <memory> = max cap, not the live currentMemory).
	out := exec.Command("virsh", "dumpxml", "--inactive", vmTarget)
	data, err := out.Output()
	if err != nil {
		return nil
	}
	var d postEditDomain
	if err := xml.Unmarshal(data, &d); err != nil {
		return nil
	}
	// Detect autostart via `virsh dominfo` (not in the XML dump).
	info, _ := exec.Command("virsh", "dominfo", vmTarget).Output()
	autostartOn := strings.Contains(string(info), "Autostart:      enable")

	fields := map[string]any{}
	// Only surface fields the operator actually asked us to touch —
	// keeps the wire response small and signals intent.
	if _, ok := params["vcpus"]; ok {
		fields["cpu_count"] = d.Vcpu
	}
	if _, ok := params["mem_mb"]; ok {
		// XML reports KiB; VM.mem_total_mb is MB.
		fields["mem_total_mb"] = int64(d.Memory) / 1024
	}
	if _, ok := params["boot_dev"]; ok {
		fields["boot_dev"] = d.OS.Boot.Dev
	}
	if _, ok := params["cpu_mode"]; ok {
		fields["cpu_mode"] = d.CPU.Mode
	}
	if _, ok := params["video_model"]; ok {
		fields["video_model"] = d.Devices.Video.Model.Type
	}
	if _, ok := params["autostart"]; ok {
		fields["autostart"] = autostartOn
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// Unused strconv guard — collect helper may need it for future
// numeric fields; keeps the import stable across refactors.
var _ = strconv.Atoi
