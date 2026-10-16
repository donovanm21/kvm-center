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
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Version is read from the committed VERSION file at compile time. The
// Dockerfile.build step copies server/VERSION → kvm-tools/VERSION so
// the daemon and the server share a single source of truth.
//
//go:embed VERSION
var versionRaw string
var Version = strings.TrimSpace(versionRaw)

// GitSHA identifies the exact source used by distributable builds.
var GitSHA = "dev"

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
	VMUUID          string           `json:"vm_uuid"`
	Hostname        string           `json:"hostname"`
	OS              string           `json:"os"`
	Kernel          string           `json:"kernel"`
	Uptime          string           `json:"uptime"`
	CPUCount        int              `json:"cpu_count"`
	MemTotalMB      uint64           `json:"mem_total_mb"`
	MemUsedMB       uint64           `json:"mem_used_mb"`
	IPs             []string         `json:"ips"`
	Interfaces      []GuestInterface `json:"interfaces,omitempty"`
	FS              []GuestFS        `json:"fs"`
	KVMToolsVersion string           `json:"kvm_tools_version,omitempty"`
	Timestamp       time.Time        `json:"timestamp"`
}

// TelemetryReply is the server's response to a telemetry POST.
//   - UpdateURL triggers a self-update (download + rename(2) + exit,
//     systemd's Restart=always brings it back on the new binary).
//   - CanonicalUUID is set when the server detected that our DMI UUID
//     collides with a different VM (classic cloned-VM footgun). The
//     daemon caches it to /var/lib/kvm-tools/uuid and uses that from
//     then on, surviving reboots.
type TelemetryReply struct {
	Status        string `json:"status"`
	UpdateURL     string `json:"update_url,omitempty"`
	CanonicalUUID string `json:"canonical_uuid,omitempty"`
}

const uuidCacheFile = "/var/lib/kvm-tools/uuid"

func main() {
	serverURL := flag.String("server", envOr("KVM_TOOLS_SERVER", "https://localhost:8190"), "kvm-center server URL")
	uuidOverride := flag.String("uuid", envOr("KVM_TOOLS_UUID", ""), "override VM UUID (use when DMI UUID doesn't match libvirt domain UUID)")
	interval := flag.Duration("interval", 30*time.Second, "report interval")
	showVersion := flag.Bool("version", false, "print version and source revision, then exit")
	flag.Parse()
	if *showVersion {
		log.Printf("kvm-tools %s (%s)", Version, GitSHA)
		return
	}

	// v5.32.4: on-boot migration of legacy http://<host>:8090 URLs to
	// https://<host>:8190. The prior release shipped the unit-rewrite
	// logic in selfUpdate(), but the code that RAN selfUpdate was the
	// older binary — it never called rewriteKVMToolsUnit. So new
	// binaries boot with the old URL still in their argv. Fix it at
	// startup: swap the runtime URL and rewrite the systemd unit so
	// the next restart also picks up the change.
	if re := regexp.MustCompile(`^http://([^/]+):8090(.*)$`); re.MatchString(*serverURL) {
		new := re.ReplaceAllString(*serverURL, "https://$1:8190$2")
		log.Printf("kvm-tools: migrating legacy server URL %s → %s (one-time)", *serverURL, new)
		*serverURL = new
		rewriteKVMToolsUnit()
	}

	// UUID resolution order:
	//   1. -uuid / $KVM_TOOLS_UUID override (operator escape hatch)
	//   2. /var/lib/kvm-tools/uuid cache (written by a previous server
	//      canonical_uuid correction or by install-kvm-tools.sh --uuid)
	//   3. /sys/class/dmi/id/product_uuid
	var uuid string
	switch {
	case *uuidOverride != "":
		uuid = strings.ToLower(strings.TrimSpace(*uuidOverride))
	case cachedUUID() != "":
		uuid = cachedUUID()
		log.Printf("kvm-tools: using cached UUID %s (from %s)", uuid, uuidCacheFile)
	default:
		var err error
		uuid, err = readDMIUUID()
		if err != nil {
			log.Fatalf("kvm-tools: cannot read DMI UUID (is this running in a VM?): %v", err)
		}
	}
	log.Printf("kvm-tools %s: vm_uuid=%s  os=%s  server=%s  interval=%s", Version, uuid, runtime.GOOS, *serverURL, *interval)

	initHTTPClient(*serverURL)

	uuid = send(*serverURL, uuid)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	for {
		select {
		case <-ticker.C:
			uuid = send(*serverURL, uuid)
		case s := <-sig:
			log.Printf("kvm-tools: received %s, shutting down", s)
			return
		}
	}
}

func cachedUUID() string {
	b, err := os.ReadFile(uuidCacheFile)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(b)))
}

func writeCachedUUID(uuid string) {
	_ = os.MkdirAll("/var/lib/kvm-tools", 0o755)
	_ = os.WriteFile(uuidCacheFile, []byte(uuid+"\n"), 0o644)
}

// httpClient is initialised at startup by initHTTPClient — when the
// server URL is https:// we default to InsecureSkipVerify and log a loud
// warning, since the server's auto-generated self-signed cert is
// trusted only by design in homelab deployments. A user who has
// installed a trusted cert can opt in to certificate verification by
// setting KVM_TOOLS_VERIFY_TLS=1 (mirrors the agent's
// KVM_CENTER_AGENT_VERIFY_TLS=1 opt-in); the default stays skip-verify
// for back-compat.
var httpClient = &http.Client{Timeout: 10 * time.Second}
var tlsInsecureWarningShown bool

func initHTTPClient(serverURL string) {
	if !strings.HasPrefix(serverURL, "https://") {
		httpClient = &http.Client{Timeout: 10 * time.Second}
		return
	}
	verifyTLS := os.Getenv("KVM_TOOLS_VERIFY_TLS") == "1"
	httpClient = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: !verifyTLS}, //nolint:gosec // homelab default; opt in via KVM_TOOLS_VERIFY_TLS=1
		},
	}
	if !verifyTLS && !tlsInsecureWarningShown {
		log.Printf("⚠ kvm-tools: TLS verification DISABLED for %s — trusting any certificate. "+
			"Acceptable for homelab servers running the auto-generated self-signed cert; "+
			"set KVM_TOOLS_VERIFY_TLS=1 (or install a trusted cert and swap the URL) when you're ready.", serverURL)
		tlsInsecureWarningShown = true
	}
}

// send posts one telemetry report and returns the UUID the daemon
// should use on the NEXT tick — normally the same one that was passed
// in, but flipped to the canonical value when the server detects a
// DMI UUID collision.
func send(serverURL, uuid string) string {
	p := collect(uuid)
	body, err := json.Marshal(p)
	if err != nil {
		log.Printf("kvm-tools: marshal error: %v", err)
		return uuid
	}
	resp, err := httpClient.Post(serverURL+"/api/guest-telemetry", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("kvm-tools: send failed: %v", err)
		return uuid
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("kvm-tools: server returned %d", resp.StatusCode)
		return uuid
	}
	var reply TelemetryReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		// Older server versions returned a flat {status:"ok"} — that's
		// still fine, just no update / canonical signal.
		return uuid
	}
	next := uuid
	if reply.CanonicalUUID != "" && !strings.EqualFold(reply.CanonicalUUID, uuid) {
		log.Printf("kvm-tools: server corrected UUID %s → %s (likely cloned DMI); caching to %s", uuid, reply.CanonicalUUID, uuidCacheFile)
		writeCachedUUID(reply.CanonicalUUID)
		next = strings.ToLower(strings.TrimSpace(reply.CanonicalUUID))
	}
	if reply.UpdateURL != "" {
		log.Printf("kvm-tools: server requested self-update from %s", reply.UpdateURL)
		if err := selfUpdate(reply.UpdateURL); err != nil {
			log.Printf("kvm-tools: self-update failed: %v", err)
		}
	}
	return next
}

// selfUpdate downloads a new kvm-tools binary and replaces the
// running executable.
//
// Linux (and macOS): plain os.Rename() onto a running ELF works —
// the kernel keeps the old inode alive for the running process, the
// new inode replaces the dirent. systemd's Restart=always relaunches
// the unit from the new binary.
//
// Windows (v5.33.5 fix): os.Rename CAN'T overwrite a running exe,
// but it CAN rename the running exe to a new name within the same
// directory (NTFS tracks files by ID, not path). So we:
//  1. download to exe.new
//  2. rename exe → exe.old  (running process keeps running at .old)
//  3. rename exe.new → exe
//  4. spawn a detached helper that kills the current process + runs
//     `schtasks /Run /TN kvm-tools` to launch the new binary
//  5. os.Exit(0)
//
// The Task Scheduler action at $BinPath then executes the new file.
// The .old file stays on disk until the next install/update or a
// reboot; the installer cleans it up.
func selfUpdate(url string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	// Self-update binary downloads reuse httpClient — if we're talking
	// to an HTTPS control plane the same skip-verify + warning applies
	// to the binary fetch, and if the build portal is plain-HTTP nothing
	// changes. Bump the timeout because binaries are bigger than JSON.
	dlClient := *httpClient
	dlClient.Timeout = 60 * time.Second
	resp, err := dlClient.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}
	// Write to a sibling tmp in the same directory so we can atomically
	// rename onto the executable (mv across filesystems would fail).
	newPath := exe + ".new"
	out, err := os.OpenFile(newPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(newPath)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Chmod(newPath, 0o755); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("chmod: %w", err)
	}

	if runtime.GOOS == "windows" {
		return swapBinaryWindows(exe, newPath)
	}

	// Unix path: rename-over is safe.
	if err := os.Rename(newPath, exe); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("rename: %w", err)
	}
	// v5.32.3: migrate any legacy systemd unit still pointing at the
	// plain-HTTP :8090 listener over to TLS :8190. The binary updates
	// in place but ExecStart baked the URL at install time, so without
	// this step a fleet rotation leaves the guests reporting on :8090
	// forever. Best-effort; skip on non-Linux and tolerate missing unit.
	rewriteKVMToolsUnit()
	log.Printf("kvm-tools: replaced %s — exiting so systemd restarts on the new binary", exe)
	// Exit cleanly so systemd's Restart=always kicks in. Sleep briefly
	// so the log line lands before the process dies.
	time.Sleep(200 * time.Millisecond)
	os.Exit(0)
	return nil
}

// swapBinaryWindows implements the rename-trick + detached helper
// flow described in the selfUpdate doc comment. Lives here (not in a
// separate _windows.go file) because os/exec's Command works on any
// platform — Task Scheduler calls happen via `schtasks` which is
// Windows-only but harmless to reference from Unix builds (never
// invoked, since swapBinaryWindows only runs when GOOS == "windows").
func swapBinaryWindows(exe, newPath string) error {
	oldPath := exe + ".old"
	// Best-effort cleanup of any leftover .old from a prior update.
	// Ignore errors — if it fails (rare, something else has it open)
	// the installer will catch it next time.
	_ = os.Remove(oldPath)

	// Step 1: rename the running exe out of the way. Windows allows
	// this even with the file open in our own process.
	if err := os.Rename(exe, oldPath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("rename running exe to .old: %w", err)
	}
	// Step 2: put the new binary in place.
	if err := os.Rename(newPath, exe); err != nil {
		// Roll back so we don't leave the machine without any binary.
		_ = os.Rename(oldPath, exe)
		return fmt.Errorf("rename new binary to exe: %w", err)
	}
	// Step 3: spawn a detached helper that waits, then runs
	// `schtasks /Run /TN kvm-tools` to relaunch this task on the
	// new binary. v5.33.5 used `cmd /c start /B` (helper inherited
	// the parent's Job object — Task Scheduler killed the whole
	// job when our action exited). v5.45.2 tried a one-shot
	// scheduled task (also failed because spawning `schtasks
	// /Create` happens *inside* the dying job, and the create call
	// itself can be killed mid-flight on the Gaming host —
	// observed on prod: no helper task ever materialised, no
	// .bat file in C:\Windows\Temp). v5.46.4 spawns directly with
	// CREATE_BREAKAWAY_FROM_JOB | DETACHED_PROCESS so the child
	// truly escapes our Job and survives our os.Exit.
	logPath := filepath.Join(os.TempDir(), "kvm-tools-update.log")
	if err := spawnRestartHelper(logPath); err != nil {
		log.Printf("kvm-tools: spawn restart helper failed: %v — task may stay Ready until reboot", err)
	} else {
		log.Printf("kvm-tools: swapped binary (old→%s, new→%s); detached restart helper spawned, log=%s", oldPath, exe, logPath)
	}
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
	return nil
}

// rewriteKVMToolsUnit edits /etc/systemd/system/kvm-tools.service in
// place, swapping the server URL's http://…:8090 for https://…:8190
// if it's still on the legacy path, and runs daemon-reload so the
// next systemd restart picks up the new ExecStart. No-op on non-Linux
// or when the unit doesn't exist (e.g. Windows guests, manual runs).
func rewriteKVMToolsUnit() {
	if runtime.GOOS != "linux" {
		return
	}
	path := "/etc/systemd/system/kvm-tools.service"
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	re := regexp.MustCompile(`http://([^\s/]+):8090`)
	rewritten := re.ReplaceAll(data, []byte("https://$1:8190"))
	if bytesEqual(rewritten, data) {
		return // nothing to do
	}
	if err := os.WriteFile(path, rewritten, 0o644); err != nil {
		log.Printf("kvm-tools: unit rewrite failed (%v) — next restart will keep legacy URL", err)
		return
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
	log.Printf("kvm-tools: systemd unit rewritten from :8090/http → :8190/https")
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
		VMUUID:          uuid,
		Hostname:        h,
		OS:              readOSPretty(),
		Kernel:          readKernel(),
		Uptime:          readUptime(),
		CPUCount:        runtime.NumCPU(),
		MemTotalMB:      memTotal,
		MemUsedMB:       memUsed,
		IPs:             ips,
		Interfaces:      ifaces,
		FS:              readFilesystems(),
		KVMToolsVersion: Version,
		Timestamp:       time.Now(),
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
