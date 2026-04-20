package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const migratorKey = "/var/lib/kvm-migrator/.ssh/id_ed25519"
const migratorUser = "kvm-migrator"
const imageDir = "/var/lib/libvirt/images"

// MigrationStatus tracks the progress of an in-flight offline migration.
type MigrationStatus struct {
	Active      bool   `json:"active"`
	MigrationID string `json:"migration_id,omitempty"`
	VMName      string `json:"vm_name,omitempty"`
	Dest        string `json:"dest,omitempty"`
	Phase       string `json:"phase"`
	Progress    int    `json:"progress"`
	Detail      string `json:"detail"`
	StartedAt   int64  `json:"started_at,omitempty"`
	Elapsed     int64  `json:"elapsed_s,omitempty"`
	Error       string `json:"error,omitempty"`
}

var (
	migStatus   MigrationStatus
	migStatusMu sync.RWMutex
	// migCancel cancels the in-flight migration's root context. The
	// offlineMigrate goroutine periodically checks ctx.Err() and routes
	// any non-nil result through the existing rollback() path so the
	// source VM is restored. Set when a migration starts; cleared when
	// it finishes (or is cancelled).
	migCancel context.CancelFunc
	// migReporter pushes progress frames to the server via gRPC so the
	// SPA's progress bar doesn't have to wait on /migrate-status polling.
	// Set on startOfflineMigrate, closed when the goroutine exits. nil
	// when the reporter couldn't open (e.g. server has no gRPC listener
	// yet) — setMigStatus skips silently in that case.
	migReporter *MigrationProgressReporter
)

// errCancelled is returned from offlineMigrate steps that observed a
// cancelled context; callers route it through rollback().
var errCancelled = errors.New("migration cancelled by operator")

func setMigStatus(phase string, progress int, detail string) {
	migStatusMu.Lock()
	migStatus.Phase = phase
	migStatus.Progress = progress
	migStatus.Detail = detail
	if migStatus.StartedAt > 0 {
		migStatus.Elapsed = time.Now().Unix() - migStatus.StartedAt
	}
	reporter := migReporter
	frame := ProgressFrame{
		MigrationID: migStatus.MigrationID,
		VMName:      migStatus.VMName,
		DestHost:    migStatus.Dest,
		Phase:       phase,
		ProgressPct: int32(progress),
		Detail:      detail,
	}
	migStatusMu.Unlock()
	if reporter != nil && frame.MigrationID != "" {
		// Best-effort; stream errors aren't fatal — HTTP /migrate-status
		// polling still covers correctness, this is just the fast path.
		_ = reporter.Send(frame)
	}
}

func getMigStatus() MigrationStatus {
	migStatusMu.RLock()
	defer migStatusMu.RUnlock()
	s := migStatus
	if s.StartedAt > 0 {
		s.Elapsed = time.Now().Unix() - s.StartedAt
	}
	return s
}

func handleMigrateStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(getMigStatus())
}

// handleCancelMigrate aborts the currently in-flight migration. The
// offlineMigrate goroutine observes the cancelled context, breaks out of
// whatever step it's on, and falls into rollback() which restores the
// source VM. For live (virsh-driven) migrations the agent runs them
// synchronously inside handleCommand; we use `virsh domjobabort` to ask
// libvirt to abort the in-flight migration on the source.
func handleCancelMigrate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	migStatusMu.Lock()
	active := migStatus.Active
	vmName := migStatus.VMName
	cancel := migCancel
	migStatusMu.Unlock()

	if !active {
		// Nothing offline is in flight. Best-effort live abort: works
		// when a `virsh migrate` is running synchronously elsewhere on
		// this agent. domjobabort is a no-op if no job is active.
		if vm := r.URL.Query().Get("vm"); vm != "" {
			out, _ := exec.Command("virsh", "domjobabort", vm).CombinedOutput()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"status":  "best-effort live cancel issued",
				"detail":  strings.TrimSpace(string(out)),
				"vm":      vm,
				"offline": false,
			})
			return
		}
		http.Error(w, "no migration in flight", http.StatusNotFound)
		return
	}

	if cancel != nil {
		cancel()
	}
	log.Printf("cancel-migrate: signalled cancellation for %s", vmName)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "cancelling",
		"vm":      vmName,
		"offline": true,
	})
}

// MTLSOptions carries v5.11.7 advanced-modal selections through to
// offlineMigrateMTLS. Zero value = previous behaviour (default pool,
// keep VM name, keep original MACs).
type MTLSOptions struct {
	TargetPool    string // destination storage pool; empty = "default"
	NewVMName     string // empty = keep source VM name
	RegenerateMAC bool   // rewrite every <mac/> in domain XML to a fresh random local MAC
}

func startOfflineMigrate(vmName, dest, migrationID string, keepRaw bool, transport string, mtlsOptions MTLSOptions) {
	migStatusMu.Lock()
	if migStatus.Active {
		migStatusMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	migCancel = cancel
	migStatus = MigrationStatus{
		Active:      true,
		MigrationID: migrationID,
		VMName:      vmName,
		Dest:        dest,
		Phase:       "starting",
		Progress:    0,
		Detail:      "Initialising offline migration",
		StartedAt:   time.Now().Unix(),
	}
	// Open the server-push stream. Best-effort: if the server doesn't
	// have the gRPC listener yet (old deploy), reporter stays nil and
	// progress continues to flow via HTTP /migrate-status polling.
	if migrationID != "" && telemetryServerURL != "" {
		grpcAddr := deriveGRPCAddr(telemetryServerURL)
		if grpcAddr != "" {
			if rep, err := OpenMigrationProgressReporter(ctx, grpcAddr); err == nil {
				migReporter = rep
			} else {
				log.Printf("migrate: gRPC progress reporter disabled (%v) — HTTP polling still covers", err)
			}
		}
	}
	migStatusMu.Unlock()

	go func() {
		var err error
		// v5.11.6: mTLS path uses virsh vol-upload over qemu+tls://
		// instead of scp. Falls back to ssh path when transport is
		// anything else or when keep_raw=true (raw conversion on
		// target still needs remote shell; revisit in v5.11.7).
		if transport == "tls" && !keepRaw {
			err = offlineMigrateMTLS(ctx, vmName, dest, mtlsOptions)
		} else {
			err = offlineMigrate(ctx, vmName, dest, keepRaw)
		}
		migStatusMu.Lock()
		switch {
		case err == nil:
			migStatus.Phase = "complete"
			migStatus.Progress = 100
			migStatus.Detail = "Migration completed successfully"
		case errors.Is(err, errCancelled) || errors.Is(ctx.Err(), context.Canceled):
			migStatus.Phase = "failed"
			migStatus.Error = "cancelled by operator"
			migStatus.Detail = "Migration cancelled — source VM restored"
		default:
			migStatus.Phase = "failed"
			migStatus.Error = err.Error()
			migStatus.Detail = "Migration failed: " + err.Error()
		}
		migStatus.Active = false
		if migStatus.StartedAt > 0 {
			migStatus.Elapsed = time.Now().Unix() - migStatus.StartedAt
		}
		migCancel = nil
		// Flush one terminal frame so the SPA sees phase=complete/failed
		// without waiting on the next poll, then close the stream.
		if migReporter != nil {
			_ = migReporter.Send(ProgressFrame{
				MigrationID: migStatus.MigrationID,
				VMName:      migStatus.VMName,
				DestHost:    migStatus.Dest,
				Phase:       migStatus.Phase,
				ProgressPct: int32(migStatus.Progress),
				Detail:      migStatus.Detail,
				Error:       migStatus.Error,
			})
			_, _, _ = migReporter.Close()
			migReporter = nil
		}
		migStatusMu.Unlock()
	}()
}

const vc = "virsh -c qemu:///system"

// checkCancel returns errCancelled if the operator has cancelled. Cheap
// to call between subprocess steps; keeps the rollback path tidy.
func checkCancel(ctx context.Context) error {
	if ctx.Err() != nil {
		return errCancelled
	}
	return nil
}

func offlineMigrate(ctx context.Context, vmName, dest string, keepRaw bool) error {
	logStep := func(msg string) {
		log.Printf("offline-migrate %s→%s: %s", vmName, dest, msg)
	}

	// Track state so rollback knows what to undo.
	wasRunning := false
	vmShutDown := false           // true once we've stopped the VM
	var sourceDiskPaths []string  // original disk paths for cleanup on success
	var transferredFiles []string // remote paths to clean up on rollback
	definedOnTarget := false
	var tmpFiles []string // local temp files to clean up

	// ── Rollback helper ──────────────────────────────────────
	rollback := func(reason string) {
		logStep("ROLLBACK: " + reason)
		setMigStatus("failed", 0, "Rolling back: "+reason)

		// 1. Remove transferred disk files on target.
		for _, f := range transferredFiles {
			sshMigrator(dest, "rm -f "+f, "")
		}
		// 2. Undefine on target if defined.
		if definedOnTarget {
			sshMigrator(dest, "fuser -k "+imageDir+"/"+vmName+"-*.qcow2 2>/dev/null; sleep 1", "")
			sshMigrator(dest, vc+" destroy "+vmName+" 2>/dev/null; "+vc+" undefine "+vmName+" 2>/dev/null", "")
		}
		// 3. Restart VM on source if we stopped it.
		if vmShutDown {
			logStep("ROLLBACK: restarting VM on source")
			if out, err := exec.Command("virsh", "start", vmName).CombinedOutput(); err != nil {
				logStep("ROLLBACK: failed to restart source VM: " + strings.TrimSpace(string(out)))
			} else {
				logStep("ROLLBACK: source VM restarted")
			}
		}
		// 4. Clean up local temp files.
		for _, f := range tmpFiles {
			os.Remove(f)
		}
	}

	// ── 1. Check source state ─────────────────────────────────
	stateOut, err := exec.Command("virsh", "domstate", vmName).Output()
	if err != nil {
		return fmt.Errorf("cannot read VM state: %w", err)
	}
	state := strings.TrimSpace(string(stateOut))
	wasRunning = (state == "running")

	// ── 2. Shut down if running ───────────────────────────────
	if wasRunning {
		setMigStatus("shutdown", 5, "Shutting down "+vmName)
		logStep("shutting down VM")
		if out, err := exec.Command("virsh", "shutdown", vmName).CombinedOutput(); err != nil {
			return fmt.Errorf("shutdown failed: %s", strings.TrimSpace(string(out)))
		}
		for i := 0; i < 60; i++ {
			if err := checkCancel(ctx); err != nil {
				rollback("cancelled while waiting for shutdown")
				return err
			}
			time.Sleep(2 * time.Second)
			s, _ := exec.Command("virsh", "domstate", vmName).Output()
			if strings.TrimSpace(string(s)) == "shut off" {
				break
			}
			setMigStatus("shutdown", 5+i/6, fmt.Sprintf("Waiting for shutdown (%ds)", (i+1)*2))
			if i == 59 {
				exec.Command("virsh", "destroy", vmName).Run()
				logStep("force-stopped after 120s timeout")
			}
		}
	}
	vmShutDown = true
	setMigStatus("shutdown", 15, vmName+" is shut off")
	logStep("VM is shut off")

	// ── 3. Dump XML ───────────────────────────────────────────
	xmlData, err := exec.Command("virsh", "dumpxml", "--inactive", vmName).Output()
	if err != nil {
		rollback("dumpxml failed")
		return fmt.Errorf("dumpxml failed: %w", err)
	}

	// ── 4. Parse disks from XML ───────────────────────────────
	type domDisk struct {
		Device string `xml:"device,attr"`
		Driver struct {
			Type string `xml:"type,attr"`
		} `xml:"driver"`
		Source struct {
			File string `xml:"file,attr"`
		} `xml:"source"`
		Target struct {
			Dev string `xml:"dev,attr"`
		} `xml:"target"`
	}
	type domXML struct {
		XMLName xml.Name  `xml:"domain"`
		Disks   []domDisk `xml:"devices>disk"`
	}
	var dom domXML
	if err := xml.Unmarshal(xmlData, &dom); err != nil {
		rollback("XML parse failed")
		return fmt.Errorf("parse XML: %w", err)
	}

	var diskCount int
	for _, d := range dom.Disks {
		if d.Device == "disk" && d.Source.File != "" {
			diskCount++
		}
	}
	if diskCount == 0 {
		diskCount = 1
	}

	// ── 5. Convert + transfer each disk ───────────────────────
	// If the source disk is raw and keepRaw is true, the disk is still
	// transferred as a compressed qcow2 intermediate (much smaller), then
	// converted back to raw on the target. This gives the best of both:
	// fast transfer + raw format on the destination.
	xmlStr := string(xmlData)
	diskIdx := 0
	for _, d := range dom.Disks {
		if d.Device != "disk" || d.Source.File == "" {
			continue
		}
		srcPath := d.Source.File
		sourceDiskPaths = append(sourceDiskPaths, srcPath)
		isRaw := d.Driver.Type == "raw"
		wantRaw := isRaw && keepRaw

		var destExt string
		if wantRaw {
			destExt = ".img"
		} else {
			destExt = ".qcow2"
		}
		flatName := vmName + "-" + d.Target.Dev + destExt
		tmpQcow := "/tmp/" + vmName + "-" + d.Target.Dev + ".qcow2"
		destPath := imageDir + "/" + flatName

		// Convert to compressed qcow2 for transfer (always, even for raw→raw).
		convertBase := 15 + (35*diskIdx)/diskCount
		convertEnd := 15 + (35*(diskIdx+1))/diskCount
		setMigStatus("convert", convertBase, fmt.Sprintf("Compressing %s (qemu-img convert -c)", d.Target.Dev))
		logStep(fmt.Sprintf("converting %s → compact qcow2", srcPath))
		if err := checkCancel(ctx); err != nil {
			rollback("cancelled before disk conversion")
			return err
		}
		start := time.Now()
		out, err := exec.CommandContext(ctx, "qemu-img", "convert", "-c", "-O", "qcow2", srcPath, tmpQcow).CombinedOutput()
		if err != nil {
			os.Remove(tmpQcow)
			rollback(fmt.Sprintf("qemu-img convert failed for %s", d.Target.Dev))
			return fmt.Errorf("qemu-img convert failed for %s: %s", d.Target.Dev, strings.TrimSpace(string(out)))
		}
		tmpFiles = append(tmpFiles, tmpQcow)

		fi, _ := os.Stat(tmpQcow)
		sizeMB := int64(0)
		if fi != nil {
			sizeMB = fi.Size() / (1024 * 1024)
		}
		setMigStatus("convert", convertEnd, fmt.Sprintf("Compressed %s to %d MB in %ds", d.Target.Dev, sizeMB, int(time.Since(start).Seconds())))
		logStep(fmt.Sprintf("compressed %s to %d MB in %ds", d.Target.Dev, sizeMB, int(time.Since(start).Seconds())))

		// Transfer the compressed qcow2 to the target.
		transferBase := 50 + (35*diskIdx)/diskCount
		transferEnd := 50 + (35*(diskIdx+1))/diskCount
		// If we need raw on target, scp to a .qcow2 tmp path, then convert remotely.
		remoteScpDest := destPath
		if wantRaw {
			remoteScpDest = imageDir + "/" + vmName + "-" + d.Target.Dev + ".qcow2.tmp"
		}
		setMigStatus("transfer", transferBase, fmt.Sprintf("Transferring %s (%d MB) to %s", d.Target.Dev, sizeMB, dest))
		logStep(fmt.Sprintf("transferring %s to %s", flatName, dest))
		if err := checkCancel(ctx); err != nil {
			os.Remove(tmpQcow)
			rollback("cancelled before disk transfer")
			return err
		}
		// Unlink any pre-existing disk at the target. Once a VM boots,
		// libvirt tightens the disk file to qemu:qemu 0600, which
		// scp-running-as-kvm-migrator can't overwrite. kvm-migrator IS
		// in the qemu group and the images dir is group-writable, so
		// it can unlink (though not directly rewrite). This turns a
		// second migration of the same VM from a hard failure into a
		// clean overwrite.
		_, _ = sshMigrator(dest, "rm -f "+shellQuote(remoteScpDest), "")
		start = time.Now()
		out, err = exec.CommandContext(ctx,
			"scp", "-i", migratorKey, "-o", "StrictHostKeyChecking=no",
			tmpQcow, fmt.Sprintf("%s@%s:%s", migratorUser, dest, remoteScpDest),
		).CombinedOutput()
		os.Remove(tmpQcow)
		if err != nil {
			rollback(fmt.Sprintf("transfer failed for %s", d.Target.Dev))
			return fmt.Errorf("transfer failed for %s: %s", d.Target.Dev, strings.TrimSpace(string(out)))
		}
		transferredFiles = append(transferredFiles, remoteScpDest)

		// If keepRaw: convert the qcow2 back to raw on the target, then remove the tmp.
		if wantRaw {
			setMigStatus("transfer", transferEnd-2, fmt.Sprintf("Converting %s back to raw on target", d.Target.Dev))
			logStep(fmt.Sprintf("converting %s back to raw on target", d.Target.Dev))
			rawOut, rawErr := sshMigrator(dest, fmt.Sprintf("qemu-img convert -O raw %s %s && rm -f %s", remoteScpDest, destPath, remoteScpDest), "")
			if rawErr != nil {
				rollback(fmt.Sprintf("raw conversion failed on target for %s", d.Target.Dev))
				return fmt.Errorf("raw conversion on target failed for %s: %s", d.Target.Dev, rawOut)
			}
			// Update tracked files: replace tmp with final.
			for i, f := range transferredFiles {
				if f == remoteScpDest {
					transferredFiles[i] = destPath
				}
			}
		}

		setMigStatus("transfer", transferEnd, fmt.Sprintf("Transferred %s in %ds", d.Target.Dev, int(time.Since(start).Seconds())))
		logStep(fmt.Sprintf("transferred %s in %ds", d.Target.Dev, int(time.Since(start).Seconds())))

		// Update XML: replace source path and driver type.
		xmlStr = strings.ReplaceAll(xmlStr, srcPath, destPath)
		if wantRaw {
			// Keep driver type='raw' in the XML (it's already raw, just path changed).
		} else if isRaw {
			// Source was raw but user chose qcow2: update driver type.
			xmlStr = strings.ReplaceAll(xmlStr, `type='raw'`, `type='qcow2'`)
			xmlStr = strings.ReplaceAll(xmlStr, `type="raw"`, `type="qcow2"`)
		}
		diskIdx++
	}

	// Clean up remaining temp files.
	for _, f := range tmpFiles {
		os.Remove(f)
	}
	tmpFiles = nil

	// Remove backingStore elements from the XML (flattened by qemu-img convert).
	for {
		start := strings.Index(xmlStr, "<backingStore")
		if start < 0 {
			break
		}
		end := strings.Index(xmlStr[start:], "</backingStore>")
		if end < 0 {
			end = strings.Index(xmlStr[start:], "/>")
			if end < 0 {
				break
			}
			xmlStr = xmlStr[:start] + xmlStr[start+end+2:]
		} else {
			xmlStr = xmlStr[:start] + xmlStr[start+end+len("</backingStore>"):]
		}
	}

	// ── 6. Prepare target ─────────────────────────────────────
	setMigStatus("define", 88, "Preparing target environment")
	logStep("preparing target")
	sshMigrator(dest, "for f in /etc/qemu-kvm/bridge.conf /etc/qemu/bridge.conf; do [ -d $(dirname $f) ] && grep -q 'allow br0' $f 2>/dev/null || echo 'allow br0' >> $f; done", "")

	// ── 7. Define VM on target ────────────────────────────────
	if err := checkCancel(ctx); err != nil {
		rollback("cancelled before defining VM on target")
		return err
	}
	setMigStatus("define", 90, "Defining VM on target")
	logStep("defining VM on target")

	out, err := sshMigrator(dest, "cat > /tmp/"+vmName+"-define.xml", xmlStr)
	if err != nil {
		rollback("failed to send XML to target")
		return fmt.Errorf("send XML to target: %s", out)
	}
	out, err = sshMigrator(dest, vc+" define /tmp/"+vmName+"-define.xml && rm /tmp/"+vmName+"-define.xml", "")
	if err != nil {
		rollback("virsh define failed on target")
		return fmt.Errorf("virsh define on target: %s", out)
	}
	definedOnTarget = true

	// ── 8. Start on target ────────────────────────────────────
	setMigStatus("start", 93, "Starting "+vmName+" on target")
	logStep("starting VM on target")
	sshMigrator(dest, "fuser -k "+imageDir+"/"+vmName+"-*.qcow2 2>/dev/null; sleep 1", "")
	out, err = sshMigrator(dest, vc+" start "+vmName, "")
	if err != nil {
		rollback("virsh start failed on target: " + out)
		return fmt.Errorf("virsh start on target: %s", out)
	}

	// ── 9. Verify VM is running on target ─────────────────────
	setMigStatus("verify", 95, "Verifying "+vmName+" is running on target")
	logStep("verifying VM on target")
	verified := false
	for i := 0; i < 10; i++ {
		time.Sleep(2 * time.Second)
		stateOut, err := sshMigrator(dest, vc+" domstate "+vmName, "")
		if err == nil && strings.TrimSpace(stateOut) == "running" {
			verified = true
			logStep("VM verified running on target")
			break
		}
		setMigStatus("verify", 95+i/2, fmt.Sprintf("Waiting for VM to start (%ds)", (i+1)*2))
	}
	if !verified {
		rollback("VM failed to reach running state on target after 20s")
		return fmt.Errorf("VM did not reach running state on target")
	}

	// ── 10. Cleanup source (VM confirmed running on target) ───
	setMigStatus("cleanup", 98, "Cleaning up source host")
	logStep("cleaning up source — VM confirmed running on target")

	// Undefine the VM on source. Match the flags DELETE_VM uses so
	// EFI-firmware VMs (NVRAM present) and VMs with managed-save or
	// snapshot metadata don't leave a ghost "shut off" entry behind,
	// which was the v5.2 Test VM bug. Log the error rather than
	// swallowing — the operator can't fix what they don't see.
	if out, err := exec.Command("virsh", "undefine", vmName,
		"--nvram", "--managed-save", "--snapshots-metadata").CombinedOutput(); err != nil {
		logStep(fmt.Sprintf("warning: virsh undefine on source failed: %s — ghost may remain, clean up manually",
			strings.TrimSpace(string(out))))
	}

	// Delete source disk files (the originals, not backing files from snapshots).
	for _, path := range sourceDiskPaths {
		if err := os.Remove(path); err != nil {
			logStep(fmt.Sprintf("warning: could not remove source disk %s: %v", path, err))
		} else {
			logStep(fmt.Sprintf("removed source disk: %s", path))
		}
	}

	return nil
}

// shellQuote wraps s in single quotes with any embedded quote
// escaped. Used when composing remote shell commands built from
// server-supplied paths so an operator-controlled disk name can't
// break out of the argument.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func sshMigrator(host, cmd, stdin string) (string, error) {
	args := []string{
		"-i", migratorKey,
		"-o", "StrictHostKeyChecking=no",
		"-o", "BatchMode=yes",
		fmt.Sprintf("%s@%s", migratorUser, host),
		cmd,
	}
	c := exec.Command("ssh", args...)
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	out, err := c.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
