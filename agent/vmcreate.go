package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// vmNameRe is the accept pattern for operator-supplied VM names. It is
// the conservative intersection of "valid libvirt domain name" and
// "safe as a bare argv/shell token": letters, digits, and the
// punctuation libvirt domains commonly use (_ . + -), 1..64 chars. No
// whitespace, no shell metacharacters, no path separators.
var vmNameRe = regexp.MustCompile(`^[A-Za-z0-9_.+-]{1,64}$`)

// validVMName rejects any VM name that could inject arguments or shell
// commands. Every operator-supplied name is passed to
// exec.Command("virsh", …) and, on the SSH migrate path, interpolated
// into remote shell strings, so a name with metacharacters or a
// leading '-' (parsed as a flag) is an injection vector. Accepted names
// match vmNameRe AND do not start with '-'. Returns a descriptive error
// naming the offending value otherwise; callers surface it to the
// operator.
func validVMName(name string) error {
	if name == "" {
		return fmt.Errorf("VM name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("VM name %q too long (max 64 characters)", name)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("VM name %q must not start with '-'", name)
	}
	if !vmNameRe.MatchString(name) {
		return fmt.Errorf("VM name %q contains invalid characters (allowed: letters, digits, and _ . + -)", name)
	}
	return nil
}

// VMCreateRequest is the JSON body for POST /vm-create.
type VMCreateRequest struct {
	Name       string `json:"name"`
	Memory     int    `json:"memory_mb"`
	VCPUs      int    `json:"vcpus"`
	DiskSizeGB int    `json:"disk_size_gb"`
	DiskFormat string `json:"disk_format"` // "qcow2" or "raw"
	OSVariant  string `json:"os_variant"`  // e.g. "rocky9", "ubuntu24.04"
	ISOPath    string `json:"iso_path"`    // path to install media (empty for import)
	ImportDisk string `json:"import_disk"` // path to existing disk image (empty for new)
	Bridge     string `json:"bridge"`      // e.g. "br0"
	PoolPath   string `json:"pool_path"`   // e.g. "/var/lib/libvirt/images"
	Firmware   string `json:"firmware"`    // "bios" or "efi"
	Autostart  bool   `json:"autostart"`
}

// handleVMCreateInfo returns host capabilities for the create-VM form.
// GET /vm-create-info
func handleVMCreateInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]any{}

	// OS variants. Initialise as empty slice — see vmcreate.go's
	// bridges/pools/isos sections for the same pattern + rationale:
	// Go nil slices serialise to JSON `null`, which crashes the SPA's
	// `.length` accessor. v5.60.13 — same fix class as v5.60.12's
	// filebrowser fix; applied here pre-emptively to every list this
	// handler returns.
	variants := []string{}
	out, err := exec.Command("virt-install", "--osinfo", "list").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			v := strings.TrimSpace(line)
			if v != "" {
				variants = append(variants, v)
			}
		}
	}
	info["os_variants"] = variants

	// Networks (bridges). v5.55.5: always scan BOTH libvirt-managed
	// networks AND OS-level bridges, deduped. Pre-v5.55.5 we only
	// fell through to the OS scan when libvirt had zero networks —
	// so a host with libvirt's `default` (wrapping `virbr0`) plus an
	// OS `br0` bridged to the physical NIC reported only `virbr0`,
	// hiding the LAN-routable bridge from the operator's VM-create
	// wizard. Operators deploying kvmc into existing hypervisor
	// estates frequently have OS-level bridges that aren't wrapped
	// by libvirt networks; both should be selectable.
	bridgeSet := make(map[string]struct{})
	out, _ = exec.Command("virsh", "net-list", "--name").Output()
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		xmlOut, err := exec.Command("virsh", "net-dumpxml", name).Output()
		if err == nil && strings.Contains(string(xmlOut), "bridge name=") {
			start := strings.Index(string(xmlOut), "bridge name='")
			if start >= 0 {
				rest := string(xmlOut)[start+len("bridge name='"):]
				end := strings.Index(rest, "'")
				if end > 0 {
					bridgeSet[rest[:end]] = struct{}{}
				}
			}
		}
	}
	out, _ = exec.Command("ip", "link", "show", "type", "bridge").Output()
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.Contains(line, "state UP") {
			name := strings.TrimRight(fields[1], ":")
			if name != "" {
				bridgeSet[name] = struct{}{}
			}
		}
	}
	bridges := make([]string, 0, len(bridgeSet))
	for b := range bridgeSet {
		bridges = append(bridges, b)
	}
	sort.Strings(bridges)
	info["bridges"] = bridges

	// Storage pools.
	type poolInfo struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	pools := []poolInfo{}
	out, _ = exec.Command("virsh", "pool-list", "--name").Output()
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		xmlOut, _ := exec.Command("virsh", "pool-dumpxml", name).Output()
		path := ""
		if start := strings.Index(string(xmlOut), "<path>"); start >= 0 {
			rest := string(xmlOut)[start+len("<path>"):]
			if end := strings.Index(rest, "</path>"); end > 0 {
				path = rest[:end]
			}
		}
		pools = append(pools, poolInfo{Name: name, Path: path})
	}
	info["pools"] = pools

	// ISOs in pools.
	isos := []string{}
	for _, p := range pools {
		matches, _ := filepath.Glob(p.Path + "/*.iso")
		isos = append(isos, matches...)
		matches, _ = filepath.Glob(p.Path + "/*.ISO")
		isos = append(isos, matches...)
	}
	info["isos"] = isos

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleVMCreate creates a new VM via virt-install.
// POST /vm-create
func handleVMCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req VMCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	if err := validVMName(req.Name); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if req.Memory == 0 || req.VCPUs == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "name, memory_mb, and vcpus are required"})
		return
	}

	if req.DiskFormat == "" {
		req.DiskFormat = "qcow2"
	}
	if req.Bridge == "" {
		req.Bridge = "br0"
	}
	if req.PoolPath == "" {
		req.PoolPath = "/var/lib/libvirt/images"
	}
	if req.Firmware == "" {
		req.Firmware = "bios"
	}

	args := []string{
		"--name", req.Name,
		"--memory", strconv.Itoa(req.Memory),
		"--vcpus", strconv.Itoa(req.VCPUs),
		"--network", "bridge=" + req.Bridge + ",model=virtio",
		"--graphics", "vnc,listen=127.0.0.1",
		"--noautoconsole",
		"--channel", "unix,target.type=virtio,target.name=org.qemu.guest_agent.0",
		"--memballoon", "virtio",
		"--rng", "/dev/urandom",
	}

	// OS variant.
	if req.OSVariant != "" {
		args = append(args, "--os-variant", req.OSVariant)
	}

	// Firmware.
	if req.Firmware == "efi" {
		args = append(args, "--boot", "uefi")
	}

	// Disk.
	if req.ImportDisk != "" {
		// Import existing disk image.
		args = append(args, "--disk", req.ImportDisk+",bus=virtio", "--import")
	} else {
		diskPath := filepath.Join(req.PoolPath, req.Name+"."+req.DiskFormat)
		diskSpec := fmt.Sprintf("path=%s,size=%d,format=%s,bus=virtio",
			diskPath, req.DiskSizeGB, req.DiskFormat)
		args = append(args, "--disk", diskSpec)
	}

	// Install media.
	if req.ISOPath != "" && req.ImportDisk == "" {
		args = append(args, "--cdrom", req.ISOPath)
	} else if req.ImportDisk == "" {
		// No ISO and no import — PXE boot.
		args = append(args, "--pxe")
	}

	log.Printf("vm-create: virt-install %s", strings.Join(args, " "))

	out, err := exec.Command("virt-install", args...).CombinedOutput()
	output := strings.TrimSpace(string(out))

	if err != nil {
		log.Printf("vm-create failed: %v — %s", err, output)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   output,
		})
		return
	}

	// Set autostart if requested.
	if req.Autostart {
		exec.Command("virsh", "autostart", req.Name).Run()
	}

	log.Printf("vm-create ok: %s", req.Name)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": fmt.Sprintf("VM '%s' created successfully", req.Name),
		"output":  output,
	})
}
