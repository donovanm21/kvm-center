package main

import (
	"encoding/json"
	"encoding/xml"
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
	Active    bool   `json:"active"`
	VMName    string `json:"vm_name,omitempty"`
	Dest      string `json:"dest,omitempty"`
	Phase     string `json:"phase"`
	Progress  int    `json:"progress"`
	Detail    string `json:"detail"`
	StartedAt int64  `json:"started_at,omitempty"`
	Elapsed   int64  `json:"elapsed_s,omitempty"`
	Error     string `json:"error,omitempty"`
}

var (
	migStatus   MigrationStatus
	migStatusMu sync.RWMutex
)

func setMigStatus(phase string, progress int, detail string) {
	migStatusMu.Lock()
	defer migStatusMu.Unlock()
	migStatus.Phase = phase
	migStatus.Progress = progress
	migStatus.Detail = detail
	if migStatus.StartedAt > 0 {
		migStatus.Elapsed = time.Now().Unix() - migStatus.StartedAt
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

func startOfflineMigrate(vmName, dest string) {
	migStatusMu.Lock()
	if migStatus.Active {
		migStatusMu.Unlock()
		return
	}
	migStatus = MigrationStatus{
		Active:    true,
		VMName:    vmName,
		Dest:      dest,
		Phase:     "starting",
		Progress:  0,
		Detail:    "Initialising offline migration",
		StartedAt: time.Now().Unix(),
	}
	migStatusMu.Unlock()

	go func() {
		err := offlineMigrate(vmName, dest)
		migStatusMu.Lock()
		if err != nil {
			migStatus.Phase = "failed"
			migStatus.Error = err.Error()
			migStatus.Detail = "Migration failed: " + err.Error()
		} else {
			migStatus.Phase = "complete"
			migStatus.Progress = 100
			migStatus.Detail = "Migration completed successfully"
		}
		migStatus.Active = false
		if migStatus.StartedAt > 0 {
			migStatus.Elapsed = time.Now().Unix() - migStatus.StartedAt
		}
		migStatusMu.Unlock()
	}()
}

const vc = "virsh -c qemu:///system"

func offlineMigrate(vmName, dest string) error {
	logStep := func(msg string) {
		log.Printf("offline-migrate %s→%s: %s", vmName, dest, msg)
	}

	// Track state so rollback knows what to undo.
	wasRunning := false
	vmShutDown := false                  // true once we've stopped the VM
	var sourceDiskPaths []string         // original disk paths for cleanup on success
	var transferredFiles []string        // remote paths to clean up on rollback
	definedOnTarget := false
	var tmpFiles []string                // local temp files to clean up

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
	xmlStr := string(xmlData)
	diskIdx := 0
	for _, d := range dom.Disks {
		if d.Device != "disk" || d.Source.File == "" {
			continue
		}
		srcPath := d.Source.File
		sourceDiskPaths = append(sourceDiskPaths, srcPath)
		flatName := vmName + "-" + d.Target.Dev + ".qcow2"
		tmpPath := "/tmp/" + flatName
		destPath := imageDir + "/" + flatName

		// Convert.
		convertBase := 15 + (35*diskIdx)/diskCount
		convertEnd := 15 + (35*(diskIdx+1))/diskCount
		setMigStatus("convert", convertBase, fmt.Sprintf("Compressing %s (qemu-img convert -c)", d.Target.Dev))
		logStep(fmt.Sprintf("converting %s → compact qcow2", srcPath))
		start := time.Now()
		out, err := exec.Command("qemu-img", "convert", "-c", "-O", "qcow2", srcPath, tmpPath).CombinedOutput()
		if err != nil {
			os.Remove(tmpPath)
			rollback(fmt.Sprintf("qemu-img convert failed for %s", d.Target.Dev))
			return fmt.Errorf("qemu-img convert failed for %s: %s", d.Target.Dev, strings.TrimSpace(string(out)))
		}
		tmpFiles = append(tmpFiles, tmpPath)

		fi, _ := os.Stat(tmpPath)
		sizeMB := int64(0)
		if fi != nil {
			sizeMB = fi.Size() / (1024 * 1024)
		}
		setMigStatus("convert", convertEnd, fmt.Sprintf("Compressed %s to %d MB in %ds", d.Target.Dev, sizeMB, int(time.Since(start).Seconds())))
		logStep(fmt.Sprintf("compressed %s to %d MB in %ds", d.Target.Dev, sizeMB, int(time.Since(start).Seconds())))

		// Transfer.
		transferBase := 50 + (35*diskIdx)/diskCount
		transferEnd := 50 + (35*(diskIdx+1))/diskCount
		setMigStatus("transfer", transferBase, fmt.Sprintf("Transferring %s (%d MB) to %s", d.Target.Dev, sizeMB, dest))
		logStep(fmt.Sprintf("transferring %s to %s:%s", flatName, dest, destPath))
		start = time.Now()
		out, err = exec.Command(
			"scp", "-i", migratorKey, "-o", "StrictHostKeyChecking=no",
			tmpPath, fmt.Sprintf("%s@%s:%s", migratorUser, dest, destPath),
		).CombinedOutput()
		os.Remove(tmpPath)
		if err != nil {
			rollback(fmt.Sprintf("transfer failed for %s", d.Target.Dev))
			return fmt.Errorf("transfer failed for %s: %s", d.Target.Dev, strings.TrimSpace(string(out)))
		}
		transferredFiles = append(transferredFiles, destPath)
		setMigStatus("transfer", transferEnd, fmt.Sprintf("Transferred %s in %ds", d.Target.Dev, int(time.Since(start).Seconds())))
		logStep(fmt.Sprintf("transferred %s in %ds", d.Target.Dev, int(time.Since(start).Seconds())))

		xmlStr = strings.ReplaceAll(xmlStr, srcPath, destPath)
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

	// Undefine the VM on source.
	exec.Command("virsh", "undefine", vmName).Run()

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
