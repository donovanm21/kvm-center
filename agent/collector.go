package main

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kvm-center/internal/models"
)

// cpuSnapshot holds one /proc/stat reading for delta calculation.
type cpuSnapshot struct {
	total uint64
	idle  uint64
}

var prevCPU cpuSnapshot
var prevVMCPU = make(map[string]struct {
	cpuNs uint64
	ts    time.Time
})

// CollectTelemetry gathers host and VM metrics using /proc and virsh.
func CollectTelemetry(agentPort int) models.TelemetryPayload {
	return models.TelemetryPayload{
		HostID:     hostname(),
		AgentPort:  agentPort,
		OS:         osRelease(),
		KernelVer:  kernelVersion(),
		LibvirtVer: libvirtVersion(),
		CPUUsage:   cpuUsage(),
		CPUCount:   runtime.NumCPU(),
		MemUsage:   memUsage(),
		MemTotalMB: memTotalMB(),
		MemFreeMB:  memFreeMB(),
		Uptime:     uptime(),
		VMs:        listVMs(),
		Storage:    listStoragePools(),
		Timestamp:  time.Now(),
	}
}

// ─── Host metrics ────────────────────────────────────────────────────────────

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func kernelVersion() string {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		return fields[2]
	}
	return ""
}

func osRelease() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return runtime.GOOS
}

func libvirtVersion() string {
	out, err := exec.Command("virsh", "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func uptime() string {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return ""
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return ""
	}
	d := time.Duration(secs) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// cpuUsage returns the host CPU usage percentage as a delta from the last call.
func cpuUsage() float64 {
	snap := readCPUStat()
	if prevCPU.total == 0 {
		prevCPU = snap
		return 0
	}
	totalDelta := snap.total - prevCPU.total
	idleDelta := snap.idle - prevCPU.idle
	prevCPU = snap
	if totalDelta == 0 {
		return 0
	}
	return math.Round((float64(totalDelta-idleDelta)/float64(totalDelta))*10000) / 100
}

func readCPUStat() cpuSnapshot {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuSnapshot{}
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		// cpu user nice system idle iowait irq softirq steal ...
		if len(fields) < 5 {
			break
		}
		vals := make([]uint64, len(fields)-1)
		for i, f := range fields[1:] {
			vals[i], _ = strconv.ParseUint(f, 10, 64)
		}
		var total uint64
		for _, v := range vals {
			total += v
		}
		return cpuSnapshot{total: total, idle: vals[3]}
	}
	return cpuSnapshot{}
}

func memTotalMB() uint64 {
	return parseMemField("MemTotal")
}

func memFreeMB() uint64 {
	return parseMemField("MemAvailable")
}

func memUsage() float64 {
	total := memTotalMB()
	free := memFreeMB()
	if total == 0 {
		return 0
	}
	used := total - free
	return math.Round((float64(used)/float64(total))*10000) / 100
}

func parseMemField(field string) uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, field+":") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseUint(fields[1], 10, 64)
				return kb / 1024 // convert kB → MB
			}
		}
	}
	return 0
}

// ─── VM metrics (via virsh) ──────────────────────────────────────────────────

// virshDomainXML holds minimal fields from `virsh dumpxml`.
type virshDomainXML struct {
	UUID   string        `xml:"uuid"`
	Disks  []virshDisk   `xml:"devices>disk"`
	Ifaces []virshIface  `xml:"devices>interface"`
}

type virshDisk struct {
	Type   string `xml:"type,attr"`
	Device string `xml:"device,attr"`
	Source struct {
		File string `xml:"file,attr"`
		Dev  string `xml:"dev,attr"`
	} `xml:"source"`
	Target struct {
		Dev string `xml:"dev,attr"`
	} `xml:"target"`
}

type virshIface struct {
	Type string `xml:"type,attr"`
	MAC  struct {
		Address string `xml:"address,attr"`
	} `xml:"mac"`
	Source struct {
		Bridge  string `xml:"bridge,attr"`
		Network string `xml:"network,attr"`
	} `xml:"source"`
	Target struct {
		Dev string `xml:"dev,attr"`
	} `xml:"target"`
}

func listVMs() []models.VM {
	out, err := exec.Command("virsh", "list", "--all").Output()
	if err != nil {
		return nil
	}

	var vms []models.VM
	lines := strings.Split(string(out), "\n")
	for _, line := range lines[2:] { // skip header rows
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "---") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		// parts[0] = id (or "-"), parts[1] = name, parts[2:] = state words
		name := parts[1]
		stateStr := strings.Join(parts[2:], " ")
		vm := buildVM(name, stateStr)
		vms = append(vms, vm)
	}
	return vms
}

func buildVM(name, stateStr string) models.VM {
	vm := models.VM{
		Name:     name,
		State:    mapVMState(stateStr),
		Networks: []models.VMNetwork{},
	}

	// Get UUID, CPU count, and memory from dominfo.
	if info := virshDomInfo(name); info != nil {
		vm.UUID = info["uuid"]
		vm.CPUCount, _ = strconv.Atoi(info["cpu(s)"])
		vm.MemTotalMB = parseKiBToMB(info["max memory"])
		vm.MemUsageMB = parseKiBToMB(info["used memory"])
	}
	if vm.UUID == "" {
		vm.UUID = name // fallback
	}

	// Parse domain XML for disk paths and network interfaces.
	xmlData, err := exec.Command("virsh", "dumpxml", name).Output()
	if err == nil {
		var domXML virshDomainXML
		if err := xml.Unmarshal(xmlData, &domXML); err == nil {
			if domXML.UUID != "" {
				vm.UUID = domXML.UUID
			}
			// Primary disk.
			for _, d := range domXML.Disks {
				if d.Device != "disk" {
					continue
				}
				path := d.Source.File
				if path == "" {
					path = d.Source.Dev
				}
				if path != "" {
					vm.DiskPath = path
					used, total := diskUsageGB(path)
					vm.DiskUsageGB = used
					vm.DiskTotalGB = total
					break
				}
			}
			// Network interfaces.
			for _, iface := range domXML.Ifaces {
				net := models.VMNetwork{
					MAC:    iface.MAC.Address,
					Bridge: iface.Source.Bridge,
				}
				if net.Bridge == "" {
					net.Bridge = iface.Source.Network
				}
				if iface.Target.Dev != "" {
					net.Interface = iface.Target.Dev
					stats := virshIfStat(name, iface.Target.Dev)
					net.RxBytes = stats[0]
					net.TxBytes = stats[1]
				}
				vm.Networks = append(vm.Networks, net)
			}
		}
	}

	// CPU usage via domstats delta.
	vm.CPUUsage = vmCPUUsage(name, vm.CPUCount)

	// Snapshot count (fast metadata read; 0 on error or unsupported disk format).
	vm.SnapshotCount = countSnapshots(name)

	return vm
}

// countSnapshots returns the number of snapshots for the given VM.
// Returns 0 if the VM has no snapshots or the disk format doesn't support them.
func countSnapshots(name string) int {
	out, err := exec.Command("virsh", "snapshot-list", name, "--name").Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func virshDomInfo(name string) map[string]string {
	out, err := exec.Command("virsh", "dominfo", name).Output()
	if err != nil {
		return nil
	}
	info := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		if idx := strings.Index(line, ":"); idx != -1 {
			key := strings.ToLower(strings.TrimSpace(line[:idx]))
			val := strings.TrimSpace(line[idx+1:])
			info[key] = val
		}
	}
	return info
}

// vmCPUUsage calculates CPU % by comparing domstats cpu.time readings.
func vmCPUUsage(name string, cpuCount int) float64 {
	out, err := exec.Command("virsh", "domstats", "--cpu-total", name).Output()
	if err != nil {
		return 0
	}
	var cpuNs uint64
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "cpu.time=") {
			val := strings.TrimPrefix(line, "cpu.time=")
			cpuNs, _ = strconv.ParseUint(val, 10, 64)
			break
		}
	}

	now := time.Now()
	prev, hasPrev := prevVMCPU[name]
	prevVMCPU[name] = struct {
		cpuNs uint64
		ts    time.Time
	}{cpuNs, now}

	if !hasPrev || cpuNs == 0 {
		return 0
	}

	elapsed := now.Sub(prev.ts).Seconds()
	if elapsed <= 0 || cpuCount <= 0 {
		return 0
	}
	deltaNs := float64(cpuNs - prev.cpuNs)
	pct := (deltaNs / (elapsed * float64(cpuCount) * 1e9)) * 100
	return math.Round(math.Min(pct, 100)*100) / 100
}

func virshIfStat(vmName, iface string) [2]uint64 {
	out, err := exec.Command("virsh", "domifstat", vmName, iface).Output()
	if err != nil {
		return [2]uint64{}
	}
	var rx, tx uint64
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		switch fields[1] {
		case "rx_bytes":
			rx, _ = strconv.ParseUint(fields[2], 10, 64)
		case "tx_bytes":
			tx, _ = strconv.ParseUint(fields[2], 10, 64)
		}
	}
	return [2]uint64{rx, tx}
}

func mapVMState(s string) models.VMState {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "running":
		return models.VMRunning
	case "shut off", "shutoff":
		return models.VMStopped
	case "paused":
		return models.VMPaused
	case "crashed":
		return models.VMCrashed
	default:
		return models.VMUnknown
	}
}

func parseKiBToMB(s string) uint64 {
	// format: "8388608 KiB"
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	unit := ""
	if len(fields) > 1 {
		unit = strings.ToLower(fields[1])
	}
	switch unit {
	case "kib", "kb":
		return n / 1024
	case "mib", "mb":
		return n
	case "gib", "gb":
		return n * 1024
	default:
		return n / (1024 * 1024) // assume bytes
	}
}

func diskUsageGB(path string) (used, total float64) {
	// Try the file's directory if the file itself doesn't exist yet.
	statPath := path
	if _, err := os.Stat(statPath); err != nil {
		// Walk up to find a directory we can stat.
		for i := len(statPath) - 1; i >= 0; i-- {
			if statPath[i] == '/' {
				statPath = statPath[:i]
				if statPath == "" {
					statPath = "/"
				}
				if _, err := os.Stat(statPath); err == nil {
					break
				}
			}
		}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(statPath, &st); err != nil {
		return 0, 0
	}
	totalBytes := st.Blocks * uint64(st.Bsize)
	freeBytes := st.Bfree * uint64(st.Bsize)
	usedBytes := totalBytes - freeBytes
	gb := 1024.0 * 1024.0 * 1024.0
	return math.Round(float64(usedBytes)/gb*100) / 100,
		math.Round(float64(totalBytes)/gb*100) / 100
}

// ─── Storage pools ───────────────────────────────────────────────────────────

func listStoragePools() []models.StoragePool {
	out, err := exec.Command("virsh", "pool-list", "--all").Output()
	if err != nil {
		return nil
	}

	var pools []models.StoragePool
	lines := strings.Split(string(out), "\n")
	for _, line := range lines[2:] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "---") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		name := fields[0]
		state := ""
		if len(fields) >= 2 {
			state = fields[1]
		}
		pool := virshPoolInfo(name, state)
		pools = append(pools, pool)
	}
	return pools
}

func virshPoolInfo(name, state string) models.StoragePool {
	pool := models.StoragePool{Name: name, State: state}

	out, err := exec.Command("virsh", "pool-info", name).Output()
	if err != nil {
		return pool
	}
	info := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		if idx := strings.Index(line, ":"); idx != -1 {
			key := strings.ToLower(strings.TrimSpace(line[:idx]))
			val := strings.TrimSpace(line[idx+1:])
			info[key] = val
		}
	}
	pool.PoolType = info["type"]
	pool.CapacityGB = parsePoolSize(info["capacity"])
	pool.AllocationGB = parsePoolSize(info["allocation"])
	pool.AvailableGB = parsePoolSize(info["available"])
	if info["state"] != "" {
		pool.State = info["state"]
	}

	// Get the pool path from pool-dumpxml.
	xmlOut, err := exec.Command("virsh", "pool-dumpxml", name).Output()
	if err == nil {
		var poolXML struct {
			Target struct {
				Path string `xml:"path"`
			} `xml:"target"`
		}
		if err := xml.Unmarshal(xmlOut, &poolXML); err == nil {
			pool.Path = poolXML.Target.Path
		}
	}

	return pool
}

// parsePoolSize parses virsh size strings like "500.00 GiB" or "1.00 TiB".
func parsePoolSize(s string) float64 {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return 0
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	switch strings.ToUpper(fields[1]) {
	case "TIB", "TB":
		return math.Round(n*1024*100) / 100
	case "GIB", "GB":
		return math.Round(n*100) / 100
	case "MIB", "MB":
		return math.Round(n/1024*100) / 100
	default:
		return math.Round(n*100) / 100
	}
}
