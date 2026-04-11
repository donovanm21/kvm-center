// Command kvm-tools is the guest-side daemon for KVM Center. It reads the VM's
// DMI UUID (matching the libvirt domain UUID), collects guest OS, kernel, IPs,
// and filesystem usage, and POSTs a GuestTelemetryPayload to the kvm-center
// server every interval. Once the server has received a report within the last
// 5 minutes, the kvm-center web UI will show "KVM Tools: running" for that VM
// and populate the Guest OS / IPs / Hostname / Filesystems sections.
//
// This daemon has no dependencies on qemu-guest-agent and does not require a
// virtio-serial channel. It only needs network reachability to the kvm-center
// server on port 8090.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// GuestFS mirrors models.GuestFS. Defined locally to keep this binary free of
// internal package imports so it can be built and distributed standalone.
type GuestFS struct {
	Mountpoint string  `json:"mountpoint"`
	FSType     string  `json:"fstype"`
	TotalGB    float64 `json:"total_gb"`
	UsedGB     float64 `json:"used_gb"`
}

type GuestTelemetryPayload struct {
	VMUUID     string    `json:"vm_uuid"`
	Hostname   string    `json:"hostname"`
	OS         string    `json:"os"`
	Kernel     string    `json:"kernel"`
	Uptime     string    `json:"uptime"`
	CPUCount   int       `json:"cpu_count"`
	MemTotalMB uint64    `json:"mem_total_mb"`
	MemUsedMB  uint64    `json:"mem_used_mb"`
	IPs        []string  `json:"ips"`
	FS         []GuestFS `json:"fs"`
	Timestamp  time.Time `json:"timestamp"`
}

func main() {
	serverURL := flag.String("server", envOr("KVM_TOOLS_SERVER", "http://10.10.100.8:8090"), "kvm-center server URL")
	interval := flag.Duration("interval", 30*time.Second, "report interval")
	flag.Parse()

	uuid, err := readDMIUUID()
	if err != nil {
		log.Fatalf("kvm-tools: cannot read DMI UUID (is this running in a VM?): %v", err)
	}
	log.Printf("kvm-tools: vm_uuid=%s  server=%s  interval=%s", uuid, *serverURL, *interval)

	send(*serverURL, uuid)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			send(*serverURL, uuid)
		case s := <-sig:
			log.Printf("kvm-tools: received %s, shutting down", s)
			return
		}
	}
}

func send(serverURL, uuid string) {
	p := collect(uuid)
	body, err := json.Marshal(p)
	if err != nil {
		log.Printf("kvm-tools: marshal error: %v", err)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(serverURL+"/api/guest-telemetry", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("kvm-tools: send failed: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("kvm-tools: server returned %d", resp.StatusCode)
	}
}

func collect(uuid string) GuestTelemetryPayload {
	h, _ := os.Hostname()
	memTotal, memUsed := readMem()
	return GuestTelemetryPayload{
		VMUUID:     uuid,
		Hostname:   h,
		OS:         readOSPretty(),
		Kernel:     readKernel(),
		Uptime:     readUptime(),
		CPUCount:   runtime.NumCPU(),
		MemTotalMB: memTotal,
		MemUsedMB:  memUsed,
		IPs:        readIPs(),
		FS:         readFilesystems(),
		Timestamp:  time.Now(),
	}
}

// readDMIUUID returns the DMI product UUID, which matches the libvirt domain UUID.
// Tries the modern path first, then falls back to dmidecode if needed.
func readDMIUUID() (string, error) {
	data, err := os.ReadFile("/sys/class/dmi/id/product_uuid")
	if err == nil {
		return strings.ToLower(strings.TrimSpace(string(data))), nil
	}
	return "", err
}

func readOSPretty() string {
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

func readKernel() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readUptime() string {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return ""
	}
	secs, _ := strconv.ParseFloat(fields[0], 64)
	d := time.Duration(secs) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

func readMem() (totalMB, usedMB uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	var total, avail uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total, _ = strconv.ParseUint(fields[1], 10, 64)
		case "MemAvailable:":
			avail, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	if total > 0 && avail <= total {
		return total / 1024, (total - avail) / 1024
	}
	return total / 1024, 0
}

func readIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return out
}

func readFilesystems() []GuestFS {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()

	realFS := map[string]bool{
		"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
		"zfs": true, "f2fs": true, "vfat": true, "ntfs": true, "exfat": true,
	}

	var out []GuestFS
	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		mount := fields[1]
		fsType := fields[2]
		if !realFS[fsType] {
			continue
		}
		if seen[mount] {
			continue
		}
		seen[mount] = true
		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil {
			continue
		}
		total := float64(st.Blocks*uint64(st.Bsize)) / (1024 * 1024 * 1024)
		free := float64(st.Bfree*uint64(st.Bsize)) / (1024 * 1024 * 1024)
		used := total - free
		out = append(out, GuestFS{
			Mountpoint: mount,
			FSType:     fsType,
			TotalGB:    round2(total),
			UsedGB:     round2(used),
		})
	}
	return out
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
