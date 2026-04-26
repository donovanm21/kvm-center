package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/models"
)

// runLiveMigrateWithProgress wraps `virsh migrate --verbose` and streams
// progress to the server over the existing StreamMigrationProgress
// gRPC handler. Parses "Migration: [ N %]" lines on stderr as they
// arrive — virsh emits them every ~1 s while dirty pages are being
// copied. Returns the combined stdout+stderr + exit error just like
// exec.CombinedOutput() so the caller's terminal-state logic is
// unchanged. If migration_id is empty (e.g. tests or legacy callers)
// or the server has no gRPC listener, progress streaming is skipped
// silently and only the terminal HTTP response carries the outcome.
func runLiveMigrateWithProgress(args []string, migrationID, vmName, dest string) ([]byte, error) {
	cmd := exec.Command("virsh", args...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Optional progress reporter. Best-effort; failure to open means
	// the server will still get the terminal HTTP response at the end.
	var rep *MigrationProgressReporter
	if migrationID != "" && telemetryServerURL != "" {
		if grpcAddr := deriveGRPCAddr(telemetryServerURL); grpcAddr != "" {
			if r, err := OpenMigrationProgressReporter(context.Background(), grpcAddr); err == nil {
				rep = r
			} else {
				log.Printf("live-migrate: gRPC progress reporter disabled (%v)", err)
			}
		}
	}

	var mu sync.Mutex
	var combined []byte
	collect := func(r io.Reader, isStderr bool) {
		scanner := newMigrateProgressScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			mu.Lock()
			combined = append(combined, line...)
			combined = append(combined, '\n')
			mu.Unlock()
			if !isStderr || rep == nil {
				continue
			}
			if pct, ok := parseMigrationProgress(line); ok {
				_ = rep.Send(ProgressFrame{
					MigrationID: migrationID,
					VMName:      vmName,
					DestHost:    dest,
					Phase:       "transfer",
					ProgressPct: int32(pct),
					Detail:      line,
				})
			}
		}
	}

	done := make(chan struct{}, 2)
	go func() { collect(stdout, false); done <- struct{}{} }()
	go func() { collect(stderr, true); done <- struct{}{} }()
	<-done
	<-done
	err := cmd.Wait()

	if rep != nil {
		// Flush a terminal frame so the SPA sees 100%/failed without
		// waiting on the next /migrate-status poll, then close.
		phase := "complete"
		pct := int32(100)
		errMsg := ""
		if err != nil {
			phase = "failed"
			pct = 0
			errMsg = err.Error()
		}
		_ = rep.Send(ProgressFrame{
			MigrationID: migrationID,
			VMName:      vmName,
			DestHost:    dest,
			Phase:       phase,
			ProgressPct: pct,
			Error:       errMsg,
		})
		_, _, _ = rep.Close()
	}
	return combined, err
}

// migrateProgressRe captures the integer part of virsh --verbose's
// migration progress ticks. The fractional tail is optional because
// virsh emits decimals ("Migration: [ 0.08 %]", "[100.00 %]").
var migrateProgressRe = regexp.MustCompile(`Migration:\s*\[\s*(\d+)(?:\.\d+)?\s*%\]`)

// parseMigrationProgress returns the integer percentage carried by a
// virsh "Migration: [N %]" line. Second return is false if the line
// isn't a progress tick or the captured integer falls outside the
// 0-100 % envelope (Tier 2 #7, v5.37.0). Pre-fix the value was
// passed through unchecked — a malformed input could push >100 into
// the SPA's progress bar geometry.
func parseMigrationProgress(line string) (int, bool) {
	m := migrateProgressRe.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	pct, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	if pct < 0 || pct > 100 {
		return 0, false
	}
	return pct, true
}

// newMigrateProgressScanner wraps bufio.Scanner with a split function
// that yields on either \n or \r. virsh --verbose separates progress
// ticks with \r so they overwrite the same terminal line; the default
// splitter would buffer every tick until the process exited.
func newMigrateProgressScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	s.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	return s
}

// powerCycleForEdit shuts the VM down (ACPI with destroy fallback) and
// returns without restarting. Used by EDIT_VM's allow_shutdown=true
// path — caller runs the config change against the shut-off VM and
// then restarts explicitly. Returns combined output + error from the
// final wait state for caller logging.
func powerCycleForEdit(vmTarget string) ([]byte, error) {
	_, _ = exec.Command("virsh", "shutdown", vmTarget).CombinedOutput()
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)
		s, _ := exec.Command("virsh", "domstate", vmTarget).Output()
		if strings.TrimSpace(string(s)) == "shut off" {
			return nil, nil
		}
	}
	// Guest ignored ACPI — force off.
	out, err := exec.Command("virsh", "destroy", vmTarget).CombinedOutput()
	time.Sleep(1 * time.Second)
	return out, err
}

// powerCycleVM performs a graceful shutdown → wait → start sequence so
// qemu is re-spawned and picks up any --config-only EDIT_VM changes
// (memory cap, vCPU max, etc). Graceful shutdown is attempted first via
// ACPI; after 60 s of waiting we fall back to `virsh destroy` so the
// operator isn't stuck behind a wedged guest. Returns combined stdout+
// stderr from the start command for caller logging.
func powerCycleVM(vmTarget string) ([]byte, error) {
	// If already off, just start.
	if state, _ := exec.Command("virsh", "domstate", vmTarget).Output(); strings.TrimSpace(string(state)) != "shut off" {
		_, _ = exec.Command("virsh", "shutdown", vmTarget).CombinedOutput()
		shutOff := false
		for i := 0; i < 30; i++ {
			time.Sleep(2 * time.Second)
			s, _ := exec.Command("virsh", "domstate", vmTarget).Output()
			if strings.TrimSpace(string(s)) == "shut off" {
				shutOff = true
				break
			}
		}
		if !shutOff {
			// Guest ignored ACPI (no agent, BSOD, etc). Force.
			_, _ = exec.Command("virsh", "destroy", vmTarget).CombinedOutput()
			time.Sleep(1 * time.Second)
		}
	}
	return exec.Command("virsh", "start", vmTarget).CombinedOutput()
}

// agentCommandPort is set by main() so on-demand telemetry refresh
// calls can stamp the right agent_port on the payload. 0 when unset
// (test or pre-boot).
var agentCommandPort int

// handleRefreshTelemetry runs one telemetry collection + send cycle
// on demand and waits for it to complete. Used by the server's
// virsh-reconcile admin endpoint so the caller can read the store
// immediately after and see the up-to-date state. Tier S #1,
// v5.32.21.
func handleRefreshTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if telemetryServerURL == "" {
		http.Error(w, "agent has no server URL configured", http.StatusPreconditionFailed)
		return
	}
	sendTelemetry(telemetryServerURL, agentCommandPort)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"refreshed":true}`))
}

// StartCommandServer listens for POST /command from the control plane.
// currentMaxMemKiB returns the persisted <memory> cap for vmTarget
// in KiB. Used by EDIT_VM's mem_mb path to decide setmem/setmaxmem
// ordering when crossing the current cap. Returns 0 on any failure.
func currentMaxMemKiB(vmTarget string) int64 {
	out, err := exec.Command("virsh", "dumpxml", "--inactive", vmTarget).Output()
	if err != nil {
		return 0
	}
	// Grep first `<memory unit='KiB'>N</memory>` block.
	s := string(out)
	i := strings.Index(s, "<memory")
	if i < 0 {
		return 0
	}
	j := strings.Index(s[i:], ">")
	if j < 0 {
		return 0
	}
	k := strings.Index(s[i+j:], "</memory>")
	if k < 0 {
		return 0
	}
	raw := strings.TrimSpace(s[i+j+1 : i+j+k])
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

// selfUpdateServerURL + selfUpdateBinaryPath are set by the caller
// so the /self-update handler knows where to pull the new binary
// from and where its own executable lives on disk. Wired in
// agent/main.go's StartCommandServer call site.
var (
	selfUpdateServerURL  string
	selfUpdateBinaryPath string
)

// SetSelfUpdateConfig seeds the /self-update endpoint's dependencies.
// Called from main() after the server URL + binary path are resolved.
func SetSelfUpdateConfig(serverURL, binaryPath string) {
	selfUpdateServerURL = serverURL
	selfUpdateBinaryPath = binaryPath
}

func StartCommandServer(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/command", handleCommand)
	mux.HandleFunc("/console/", handleConsolePage)
	mux.HandleFunc("/ws/", handleConsoleWS)
	mux.HandleFunc("/screenshot/", handleVMScreenshot)
	mux.HandleFunc("/snapshots/", handleSnapshotList)
	mux.HandleFunc("/migrate-status", handleMigrateStatus)
	// v5.11: destination-side endpoint that validates + caches an
	// incoming migration grant right before libvirtd handshakes with
	// the source over qemu+tls.
	mux.HandleFunc("/prepare-grant", handlePrepareGrant)
	// v5.11.1: consulted by /etc/libvirt/hooks/qemu to enforce that a
	// migration the dest is about to accept was actually authorised.
	mux.HandleFunc("/grant-active", handleGrantActive)
	mux.HandleFunc("/cancel-migrate", handleCancelMigrate)
	mux.HandleFunc("/self-update", handleSelfUpdate)
	mux.HandleFunc("/storage-browse", handleStorageBrowse)
	mux.HandleFunc("/storage-upload", handleStorageUpload)
	mux.HandleFunc("/storage-file", handleStorageDelete)
	mux.HandleFunc("/vm-create-info", handleVMCreateInfo)
	mux.HandleFunc("/vm-create", handleVMCreate)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	// v5.32.21, Tier S #1: on-demand telemetry refresh. Admin hits
	// /api/admin/reconcile/{host} on the server, server proxies to
	// this endpoint, agent collects + POSTs telemetry synchronously.
	// Returns after the telemetry call completes so the caller can
	// diff before/after.
	mux.HandleFunc("/refresh-telemetry", handleRefreshTelemetry)
	log.Printf("agent command server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("commander: %v", err)
	}
}

func handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req models.CommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond(w, false, "invalid JSON: "+err.Error())
		return
	}

	// Idempotency replay check (v5.32.17, Tier S #4). If we've seen
	// this request_id within outcomeCacheTTL, return the cached
	// response verbatim instead of re-running the command. Empty
	// request_id = pre-5.32.17 server or direct curl — no caching.
	if req.RequestID != "" {
		if e, ok := outcomes.get(req.RequestID); ok {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Command-Replayed", "true")
			w.WriteHeader(e.status)
			_, _ = w.Write(e.body)
			log.Printf("command: replay req_id=%s action=%s status=%d", req.RequestID, req.Action, e.status)
			return
		}
		// Wrap the writer so the outcome cache records the response
		// when the handler returns.
		rec := &recorderWriter{ResponseWriter: w}
		defer func() {
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			outcomes.put(req.RequestID, rec.status, rec.body)
		}()
		w = rec
	}

	vmTarget := req.VMName
	if vmTarget == "" {
		vmTarget = req.VMUUID
	}
	if vmTarget == "" {
		respond(w, false, "vm_name or vm_uuid required")
		return
	}

	log.Printf("command: action=%s vm=%s", req.Action, vmTarget)

	var (
		out []byte
		err error
	)

	switch strings.ToUpper(req.Action) {
	case "START":
		out, err = exec.Command("virsh", "start", vmTarget).CombinedOutput()
	case "STOP":
		// Prefer graceful shutdown; fall back to destroy after 30s (handled by the caller).
		out, err = exec.Command("virsh", "shutdown", vmTarget).CombinedOutput()
	case "FORCESTOP":
		out, err = exec.Command("virsh", "destroy", vmTarget).CombinedOutput()
	case "RESTART":
		out, err = exec.Command("virsh", "reboot", vmTarget).CombinedOutput()
	case "POWER_CYCLE":
		// v5.30.0: full power-cycle — shutdown + wait + start. Unlike
		// `virsh reboot` (RESTART) this actually re-spawns qemu so
		// EDIT_VM config changes like the memory cap and vCPU max take
		// effect. RESTART maps to an in-guest ACPI reboot which keeps
		// the same qemu process, so setmaxmem/--config never applied.
		out, err = powerCycleVM(vmTarget)
	case "SNAPSHOT":
		snapName := req.Params["name"]
		if snapName == "" {
			snapName = fmt.Sprintf("snap-%d", timeNow())
		}
		out, err = exec.Command("virsh", "snapshot-create-as", vmTarget, snapName).CombinedOutput()
	case "REVERT_SNAPSHOT":
		snapName := req.Params["name"]
		if snapName == "" {
			respond(w, false, "REVERT_SNAPSHOT requires params.name")
			return
		}
		out, err = exec.Command("virsh", "snapshot-revert", vmTarget, snapName, "--force").CombinedOutput()
	case "EDIT_VM":
		handleEditVM(w, req, vmTarget)
		return
	case "CHANGE_MEDIA":
		// Live ISO swap on a CDROM target. Params:
		//   media_action = "insert" | "eject"
		//   target       = libvirt device name (e.g. "hda", "sdb")
		//   path         = pool-resident ISO path (insert only)
		action := strings.ToLower(strings.TrimSpace(req.Params["media_action"]))
		target := strings.TrimSpace(req.Params["target"])
		if target == "" {
			respond(w, false, "CHANGE_MEDIA requires params.target (CDROM device name)")
			return
		}
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
	case "DELETE_SNAPSHOT":
		snapName := req.Params["name"]
		if snapName == "" {
			respond(w, false, "DELETE_SNAPSHOT requires params.name")
			return
		}
		out, err = exec.Command("virsh", "snapshot-delete", vmTarget, snapName).CombinedOutput()
		if err != nil {
			// External snapshots with backing chains can't be fully deleted while
			// the VM is running. Fall back to --metadata (removes the snapshot
			// record but preserves the backing disk files).
			metaOut, metaErr := exec.Command("virsh", "snapshot-delete", vmTarget, snapName, "--metadata").CombinedOutput()
			if metaErr == nil {
				msg := "Snapshot metadata removed (external snapshot — disk backing files preserved): " + strings.TrimSpace(string(metaOut))
				log.Printf("command ok: DELETE_SNAPSHOT (metadata) vm=%s snap=%s", vmTarget, snapName)
				respond(w, true, msg)
				return
			}
		}
	case "MIGRATE":
		handleMigrate(w, req, vmTarget)
		return
	case "DELETE_VM":
		handleDeleteVM(w, req, vmTarget)
		return
	case "OFFLINE_MIGRATE":
		handleOfflineMigrate(w, req, vmTarget)
		return
	default:
		respond(w, false, "unknown action: "+req.Action)
		return
	}

	msg := strings.TrimSpace(string(out))
	if err != nil {
		log.Printf("command failed: %s: %v — %s", req.Action, err, msg)
		respond(w, false, msg)
		return
	}
	// For live MIGRATE the combined output is a flood of `Migration:
	// [ N %]` progress lines from virsh --verbose. Useful for the log
	// but noisy as a toast/SPA response — collapse to a clean summary.
	// The already-streamed gRPC frames carry the granular progress.
	if strings.ToUpper(req.Action) == "MIGRATE" {
		log.Printf("command ok: MIGRATE vm=%s (virsh progress lines suppressed in response)", vmTarget)
		respond(w, true, "live migration completed")
		return
	}
	log.Printf("command ok: %s vm=%s: %s", req.Action, vmTarget, msg)
	respond(w, true, msg)
}

// migrationURI builds the libvirt URI for a live migration. Transport
// selector (v5.10.1):
//
//	"tls" — qemu+tls://<dest>/system. Requires libvirtd on both ends
//	        to have been configured with the cluster CA + a cert (see
//	        install-agent.sh --enroll-token). This is the default once
//	        both hypervisors are enrolled.
//	""/"ssh" — legacy qemu+ssh:// via the shared kvm-migrator keypair.
//	        Retained as a fallback for mixed fleets during v5.10.x
//	        rollout. Will be removed in v5.11.
func migrationURI(dest, transport string) string {
	if transport == "tls" {
		// verify=1 (default) — libvirtd enforces chain validation against
		// /etc/pki/CA/cacert.pem on both sides. tls_port=16514 is libvirt's
		// default TLS listener port.
		return fmt.Sprintf("qemu+tls://%s/system", dest)
	}
	return fmt.Sprintf("qemu+ssh://kvm-migrator@%s/system?keyfile=/var/lib/kvm-migrator/.ssh/id_ed25519&no_verify=1", dest)
}

// collectVMDiskPaths scrapes the domain XML for the disk source paths so
// we can unlink them after undefine when the disks weren't part of a
// libvirt storage pool (very common for qcow2 images under
// /var/lib/libvirt/images/). Returns an empty slice on any parse failure
// — the caller already guarded the undefine itself.
func collectVMDiskPaths(vmTarget string) []string {
	xml, err := exec.Command("virsh", "dumpxml", vmTarget).CombinedOutput()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(xml), "\n") {
		line = strings.TrimSpace(line)
		// <source file='/var/lib/libvirt/images/vm.qcow2'/>
		if !strings.HasPrefix(line, "<source ") {
			continue
		}
		const key = "file='"
		i := strings.Index(line, key)
		if i < 0 {
			continue
		}
		rest := line[i+len(key):]
		j := strings.IndexByte(rest, '\'')
		if j <= 0 {
			continue
		}
		paths = append(paths, rest[:j])
	}
	return paths
}

func removeDiskFile(path string) error {
	// Only operate on files under the common libvirt image directories.
	// Refuse anything else so a compromised control plane can't use this
	// as an arbitrary-delete primitive.
	safe := false
	for _, prefix := range []string{"/var/lib/libvirt/images/", "/var/lib/libvirt/boot/", "/tmp/"} {
		if strings.HasPrefix(path, prefix) {
			safe = true
			break
		}
	}
	if !safe {
		return fmt.Errorf("refusing to remove %q: outside allowed libvirt image paths", path)
	}
	return os.Remove(path)
}

func respond(w http.ResponseWriter, success bool, message string) {
	respondWithFlags(w, success, message, false)
}

// respondWithFlags is the fuller variant that can carry the
// RequiresReboot signal. EDIT_VM uses this when live-apply fails but
// --config worked: the SPA surfaces a shutdown + reboot prompt so
// the operator doesn't stare at a "success" toast while wondering
// why nothing changed.
func respondWithFlags(w http.ResponseWriter, success bool, message string, requiresReboot bool) {
	respondWithFlagsAndFields(w, success, message, requiresReboot, nil)
}

// respondWithFlagsAndFields adds the UpdatedFields map for commands
// that want the server to refresh its cached VM record immediately.
func respondWithFlagsAndFields(w http.ResponseWriter, success bool, message string, requiresReboot bool, updated map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if !success {
		w.WriteHeader(http.StatusInternalServerError)
	}
	json.NewEncoder(w).Encode(models.CommandResponse{
		Success:        success,
		Message:        message,
		RequiresReboot: requiresReboot,
		UpdatedFields:  updated,
	})
}

func timeNow() int64 {
	return time.Now().Unix()
}
