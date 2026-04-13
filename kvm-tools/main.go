// Command kvm-tools is the guest-side daemon for KVM Center. It reads the VM's
// DMI UUID (matching the libvirt domain UUID), collects guest OS, kernel, IPs,
// and filesystem usage, and POSTs a GuestTelemetryPayload to the kvm-center
// server every interval. Once the server has received a report within the last
// 5 minutes, the kvm-center web UI will show "KVM Tools: running" for that VM
// and populate the Guest OS / IPs / Hostname / Filesystems sections.
//
// Builds for both linux and windows targets via build-tagged collect_*.go files.
package main

import (
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
	"strings"
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

type GuestInterface struct {
	Name  string   `json:"name"`
	MAC   string   `json:"mac,omitempty"`
	Addrs []string `json:"addrs,omitempty"`
	Kind  string   `json:"kind"` // primary | docker | container | vpn | bridge | other
}

type GuestTelemetryPayload struct {
	VMUUID     string           `json:"vm_uuid"`
	Hostname   string           `json:"hostname"`
	OS         string           `json:"os"`
	Kernel     string           `json:"kernel"`
	Uptime     string           `json:"uptime"`
	CPUCount   int              `json:"cpu_count"`
	MemTotalMB uint64           `json:"mem_total_mb"`
	MemUsedMB  uint64           `json:"mem_used_mb"`
	IPs        []string         `json:"ips"`
	Interfaces []GuestInterface `json:"interfaces,omitempty"`
	FS         []GuestFS        `json:"fs"`
	Timestamp  time.Time        `json:"timestamp"`
}

func main() {
	serverURL := flag.String("server", envOr("KVM_TOOLS_SERVER", "http://REDACTED-HOST:8090"), "kvm-center server URL")
	uuidOverride := flag.String("uuid", envOr("KVM_TOOLS_UUID", ""), "override VM UUID (use when DMI UUID doesn't match libvirt domain UUID)")
	interval := flag.Duration("interval", 30*time.Second, "report interval")
	flag.Parse()

	var uuid string
	if *uuidOverride != "" {
		uuid = strings.ToLower(strings.TrimSpace(*uuidOverride))
	} else {
		var err error
		uuid, err = readDMIUUID()
		if err != nil {
			log.Fatalf("kvm-tools: cannot read DMI UUID (is this running in a VM?): %v", err)
		}
	}
	log.Printf("kvm-tools: vm_uuid=%s  os=%s  server=%s  interval=%s", uuid, runtime.GOOS, *serverURL, *interval)

	send(*serverURL, uuid)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

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
	ifaces := readInterfaces()
	// Keep IPs as the legacy flat list — derived from the same scan so
	// older server versions that only read .ips still work.
	var ips []string
	for _, i := range ifaces {
		ips = append(ips, i.Addrs...)
	}
	return GuestTelemetryPayload{
		VMUUID:     uuid,
		Hostname:   h,
		OS:         readOSPretty(),
		Kernel:     readKernel(),
		Uptime:     readUptime(),
		CPUCount:   runtime.NumCPU(),
		MemTotalMB: memTotal,
		MemUsedMB:  memUsed,
		IPs:        ips,
		Interfaces: ifaces,
		FS:         readFilesystems(),
		Timestamp:  time.Now(),
	}
}

// readInterfaces returns every up, non-loopback NIC on the guest with
// a heuristic Kind so the UI can flag Docker bridges and VPN tunnels.
// Loopback and down interfaces are skipped — they're noise. The first
// "real" NIC (no docker/container/vpn/bridge prefix) is tagged "primary".
func readInterfaces() []GuestInterface {
	raw, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []GuestInterface
	primaryAssigned := false
	for _, i := range raw {
		if i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		var addrStrs []string
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
			addrStrs = append(addrStrs, ip.String())
		}
		// Skip interfaces with no usable addresses unless they're a
		// recognisable bridge / docker / vpn — those are still useful
		// to surface so the operator knows they exist.
		kind := classifyInterface(i.Name)
		if len(addrStrs) == 0 && kind == "other" {
			continue
		}
		if !primaryAssigned && kind == "other" {
			kind = "primary"
			primaryAssigned = true
		}
		out = append(out, GuestInterface{
			Name:  i.Name,
			MAC:   i.HardwareAddr.String(),
			Addrs: addrStrs,
			Kind:  kind,
		})
	}
	return out
}

// classifyInterface buckets a NIC by name so the UI can colour-code
// Docker / container / VPN traffic distinctly from the primary uplink.
// Patterns chosen from common defaults: docker0, br-<hex>, veth*,
// virbr*, cni*, flannel*, weave, cali*, tailscale*, wg*, tun*, tap*.
func classifyInterface(name string) string {
	n := strings.ToLower(name)
	switch {
	case n == "docker0", strings.HasPrefix(n, "br-"):
		return "docker"
	case strings.HasPrefix(n, "veth"),
		strings.HasPrefix(n, "cni"),
		strings.HasPrefix(n, "flannel"),
		strings.HasPrefix(n, "weave"),
		strings.HasPrefix(n, "cali"),
		strings.HasPrefix(n, "podman"),
		strings.HasPrefix(n, "cnio"):
		return "container"
	case strings.HasPrefix(n, "wg"),
		strings.HasPrefix(n, "tailscale"),
		strings.HasPrefix(n, "tun"),
		strings.HasPrefix(n, "tap"),
		strings.HasPrefix(n, "zt"),
		strings.HasPrefix(n, "ppp"):
		return "vpn"
	case strings.HasPrefix(n, "virbr"), strings.HasPrefix(n, "br"):
		return "bridge"
	}
	return "other"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// formatUptime turns a duration in seconds into "Xd Yh Zm".
func formatUptime(secs float64) string {
	d := time.Duration(secs) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}
