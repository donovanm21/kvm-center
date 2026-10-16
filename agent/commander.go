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
	// Mutating, server-initiated routes require a valid command-auth
	// signature (C1) once the server advertises that it signs (see
	// command_auth.go). Read-only routes (/command-outcome, /migrate-status,
	// /snapshots, /screenshot, /vm-create-info, /health) and routes with
	// their own auth or non-server callers (/console + /ws use console
	// tokens; /prepare-grant + /grant-active are called by the libvirt qemu
	// hook and peer agents) are intentionally left unwrapped.
	mux.HandleFunc("/command", requireCommandAuth(handleCommand))
	// v5.47.2: read-only lookup for the server's reconciler. When the
	// initial POST /command response is lost (transport error /
	// timeout / 5xx), the server polls this endpoint to recover the
	// actual outcome from the agent's outcome cache.
	mux.HandleFunc("/command-outcome", handleCommandOutcome)
	// v5.48 (PR-2f): operator-driven cancellation. POST with
	// ?request_id=<id> + optional `{"reason":"..."}` body. SIGTERM
	// child, 5 s grace, SIGKILL. For MIGRATE/OFFLINE_MIGRATE we
	// also call `virsh domjobabort` so libvirtd can wind its state
	// machine down cleanly. Idempotent — already-terminal rows
	// return their existing state.
	mux.HandleFunc("/command/cancel", requireCommandAuth(handleCancelCommand))
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
	mux.HandleFunc("/cancel-migrate", requireCommandAuth(handleCancelMigrate))
	mux.HandleFunc("/self-update", requireCommandAuth(handleSelfUpdate))
	mux.HandleFunc("/storage-browse", requireCommandAuth(handleStorageBrowse))
	mux.HandleFunc("/storage-upload", requireCommandAuth(handleStorageUpload))
	mux.HandleFunc("/storage-file", requireCommandAuth(handleStorageDelete))
	mux.HandleFunc("/vm-create-info", handleVMCreateInfo)
	mux.HandleFunc("/vm-create", requireCommandAuth(handleVMCreate))
	// v5.58.0 — storage-pool lifecycle. Matching server routes are
	// under /api/hosts/<id>/storage/pools/...
	mux.HandleFunc("/pool-define", requireCommandAuth(handlePoolDefine))
	mux.HandleFunc("/pool/", requireCommandAuth(func(w http.ResponseWriter, r *http.Request) {
		// Path shapes:
		//   POST /pool/<name>/<action>     → handlePoolAction
		//   DELETE /pool/<name>            → handlePoolDelete
		rest := strings.TrimPrefix(r.URL.Path, "/pool/")
		rest = strings.TrimSuffix(rest, "/")
		if r.Method == http.MethodDelete && !strings.Contains(rest, "/") {
			handlePoolDelete(w, r)
			return
		}
		handlePoolAction(w, r)
	}))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	// v5.32.21, Tier S #1: on-demand telemetry refresh. Admin hits
	// /api/admin/reconcile/{host} on the server, server proxies to
	// this endpoint, agent collects + POSTs telemetry synchronously.
	// Returns after the telemetry call completes so the caller can
	// diff before/after.
	mux.HandleFunc("/refresh-telemetry", requireCommandAuth(handleRefreshTelemetry))
	log.Printf("agent command server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		// Bail loud — without the command server the agent can still
		// post telemetry but not accept any operator commands, which
		// will manifest as silent timeouts in the WebUI. Better to
		// crash so systemd restarts and surfaces the issue.
		log.Fatalf("commander: listen %s failed (port already in use?): %v", addr, err)
	}
}

func handleCommand(w http.ResponseWriter, r *http.Request) {
	handleCommandWithExecutor(w, r, runCommandBody)
}

func handleCommandWithExecutor(w http.ResponseWriter, r *http.Request, execute func(http.ResponseWriter, models.CommandRequest)) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req models.CommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond(w, false, "invalid JSON: "+err.Error())
		return
	}

	if req.RequestID == "" {
		// Legacy callers without an ID cannot participate in deduplication.
		execute(w, req)
		return
	}
	replay, running, err := claimCommand(req)
	if err != nil {
		log.Printf("command: cannot claim req_id=%s: %v", req.RequestID, err)
		http.Error(w, "command state unavailable; retry with the same request_id", http.StatusServiceUnavailable)
		return
	}
	if replay != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Command-Replayed", "true")
		w.WriteHeader(replay.status)
		_, _ = w.Write(replay.body)
		return
	}
	if running {
		respondAccepted(w, req.RequestID)
		return
	}
	if asyncEnabled() && asyncEligibleAction(req.Action) {
		respondAccepted(w, req.RequestID)
		go runCommandBodyAsyncWithExecutor(req, execute)
		return
	}
	// Buffer both paths so a panic after a partial response cannot be
	// cached as success or sent with an already-committed success status.
	cap := executeClaimedCommand(req, execute)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(cap.status)
	_, _ = w.Write(cap.body)
}

// runCommandBody is the action dispatcher — vmTarget validation +
// the per-action switch. Extracted so the async path (PR-2e) can
// drive it from a goroutine against a captureResponseWriter.
func runCommandBody(w http.ResponseWriter, req models.CommandRequest) {
	// Host-level actions are dispatched before the vmTarget check
	// because they don't operate on a single VM.
	if strings.ToUpper(req.Action) == "RETIRE" {
		handleRetire(w, req)
		return
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

	bgCtx := context.Background()
	switch strings.ToUpper(req.Action) {
	case "START":
		out, err = runWithWatchdog(bgCtx, req, "START", "virsh", "start", vmTarget)
	case "STOP":
		// Prefer graceful shutdown; fall back to destroy after 30s (handled by the caller).
		out, err = runWithWatchdog(bgCtx, req, "STOP", "virsh", "shutdown", vmTarget)
	case "FORCESTOP":
		out, err = runWithWatchdog(bgCtx, req, "FORCESTOP", "virsh", "destroy", vmTarget)
	case "RESTART":
		out, err = runWithWatchdog(bgCtx, req, "RESTART", "virsh", "reboot", vmTarget)
	case "POWER_CYCLE":
		// v5.30.0: full power-cycle — shutdown + wait + start. Unlike
		// `virsh reboot` (RESTART) this actually re-spawns qemu so
		// EDIT_VM config changes like the memory cap and vCPU max take
		// effect. RESTART maps to an in-guest ACPI reboot which keeps
		// the same qemu process, so setmaxmem/--config never applied.
		// Watchdog applies to the whole sequence (shutdown + wait + start).
		out, err = powerCycleVM(vmTarget)
	case "SNAPSHOT":
		snapName := req.Params["name"]
		if snapName == "" {
			snapName = fmt.Sprintf("snap-%d", timeNow())
		}
		out, err = runWithWatchdog(bgCtx, req, "SNAPSHOT", "virsh", "snapshot-create-as", vmTarget, snapName)
	case "REVERT_SNAPSHOT":
		snapName := req.Params["name"]
		if snapName == "" {
			respond(w, false, "REVERT_SNAPSHOT requires params.name")
			return
		}
		out, err = runWithWatchdog(bgCtx, req, "REVERT_SNAPSHOT", "virsh", "snapshot-revert", vmTarget, snapName, "--force")
	case "EDIT_VM":
		handleEditVM(w, req, vmTarget)
		return
	case "CHANGE_MEDIA":
		handleChangeMedia(w, req, vmTarget)
		return
	case "DELETE_SNAPSHOT":
		handleDeleteSnapshot(w, req, vmTarget)
		return
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

// handleCommandOutcome is the read-only lookup the server's
// reconciler hits when its POST /command call timed out or got a
// 5xx. We answer from the in-memory outcome cache (10 m TTL pre-
// 5.47.2; bumped to 35 m in this release so the cache always
// outlives the server's 30 m reconcile window).
//
// 200 → cached outcome; 404 → request_id not in cache (either it
// never reached us, the agent restarted, or the cache window passed
// — the server's reconciler keeps the entry pending until its own
// deadline expires, then transitions to "unknown").
//
// Note: this endpoint is mounted on the existing :8081 mux and so
// inherits whatever auth posture that mux has. The mux is bound to
// the management LAN today; once the gRPC mTLS rollout completes
// (Tier A2 in the v5.47 roadmap proposals), this endpoint inherits
// the same authn/authz.
func handleCommandOutcome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("request_id")
	if id == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing request_id"})
		return
	}
	status, body, cachedAt, ok := outcomes.lookup(id)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":      "unknown request_id",
			"request_id": id,
		})
		return
	}
	resp := models.CommandOutcomeResponse{
		RequestID:  id,
		StatusCode: status,
		CachedAt:   cachedAt,
	}
	switch {
	case status >= 200 && status < 300:
		resp.Outcome = "success"
	case status >= 400:
		resp.Outcome = "failed"
	default:
		resp.Outcome = "unknown"
	}
	// Best-effort: extract Message from the cached body so the
	// reconciler doesn't have to re-parse the agent's response shape.
	var parsed struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	resp.Message = parsed.Message
	if resp.Outcome == "failed" {
		if parsed.Error != "" {
			resp.Error = parsed.Error
		} else if parsed.Message != "" {
			resp.Error = parsed.Message
		} else {
			resp.Error = strings.TrimSpace(string(body))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
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
