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
	Phase     string `json:"phase"`     // idle, shutdown, convert, transfer, define, start, complete, failed
	Progress  int    `json:"progress"`  // 0-100
	Detail    string `json:"detail"`    // human-readable current step
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

// startOfflineMigrate launches an async offline migration and immediately returns.
// Progress is tracked via getMigStatus() / handleMigrateStatus.
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

// offlineMigrate runs the full pipeline synchronously. Returns nil on success.
func offlineMigrate(vmName, dest string) error {
	logStep := func(msg string) {
		log.Printf("offline-migrate %s→%s: %s", vmName, dest, msg)
	}

	// ── 1. Check source state ─────────────────────────────────
	stateOut, err := exec.Command("virsh", "domstate", vmName).Output()
	if err != nil {
		return fmt.Errorf("cannot read VM state: %w", err)
	}
	state := strings.TrimSpace(string(stateOut))

	// ── 2. Shut down if running ───────────────────────────────
	if state == "running" {
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
	setMigStatus("shutdown", 15, vmName+" is shut off")
	logStep("VM is shut off")

	// ── 3. Dump XML ───────────────────────────────────────────
	xmlData, err := exec.Command("virsh", "dumpxml", "--inactive", vmName).Output()
	if err != nil {
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
		return fmt.Errorf("parse XML: %w", err)
	}

	// Count actual disks for progress calculation.
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
		flatName := vmName + "-" + d.Target.Dev + ".qcow2"
		tmpPath := "/tmp/" + flatName
		destPath := imageDir + "/" + flatName

		// Phase: convert (15-50%)
		convertBase := 15 + (35*diskIdx)/diskCount
		convertEnd := 15 + (35*(diskIdx+1))/diskCount
		setMigStatus("convert", convertBase, fmt.Sprintf("Compressing %s (qemu-img convert -c)", d.Target.Dev))
		logStep(fmt.Sprintf("converting %s → compact qcow2", srcPath))
		start := time.Now()
		out, err := exec.Command(
			"qemu-img", "convert", "-c", "-O", "qcow2", srcPath, tmpPath,
		).CombinedOutput()
		if err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("qemu-img convert failed for %s: %s", d.Target.Dev, strings.TrimSpace(string(out)))
		}

		fi, _ := os.Stat(tmpPath)
		sizeMB := int64(0)
		if fi != nil {
			sizeMB = fi.Size() / (1024 * 1024)
		}
		setMigStatus("convert", convertEnd, fmt.Sprintf("Compressed %s to %d MB in %ds", d.Target.Dev, sizeMB, int(time.Since(start).Seconds())))
		logStep(fmt.Sprintf("compressed %s to %d MB in %ds", d.Target.Dev, sizeMB, int(time.Since(start).Seconds())))

		// Phase: transfer (50-85%)
		transferBase := 50 + (35*diskIdx)/diskCount
		transferEnd := 50 + (35*(diskIdx+1))/diskCount
		setMigStatus("transfer", transferBase, fmt.Sprintf("Transferring %s (%d MB) to %s", d.Target.Dev, sizeMB, dest))
		logStep(fmt.Sprintf("transferring %s to %s:%s", flatName, dest, destPath))
		start = time.Now()

		// Try xz pipe for better compression ratio; fall back to scp.
		_, xzErr := exec.LookPath("xz")
		if xzErr == nil {
			pipeCmd := fmt.Sprintf(
				"xz -1 -T0 -c %s | ssh -i %s -o StrictHostKeyChecking=no %s@%s 'xz -d -T0 > %s'",
				tmpPath, migratorKey, migratorUser, dest, destPath)
			out, err = exec.Command("bash", "-c", pipeCmd).CombinedOutput()
		} else {
			out, err = exec.Command(
				"scp", "-i", migratorKey, "-o", "StrictHostKeyChecking=no",
				tmpPath,
				fmt.Sprintf("%s@%s:%s", migratorUser, dest, destPath),
			).CombinedOutput()
		}
		os.Remove(tmpPath)
		if err != nil {
			return fmt.Errorf("transfer failed for %s: %s", d.Target.Dev, strings.TrimSpace(string(out)))
		}
		setMigStatus("transfer", transferEnd, fmt.Sprintf("Transferred %s in %ds", d.Target.Dev, int(time.Since(start).Seconds())))
		logStep(fmt.Sprintf("transferred %s in %ds", d.Target.Dev, int(time.Since(start).Seconds())))

		xmlStr = strings.ReplaceAll(xmlStr, srcPath, destPath)
		diskIdx++
	}

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

	// ── 6. Prepare target environment ─────────────────────────
	setMigStatus("define", 88, "Preparing target environment")
	logStep("preparing target")
	sshMigrator(dest, "sudo mkdir -p /etc/qemu && grep -q 'allow br0' /etc/qemu/bridge.conf 2>/dev/null || echo 'allow br0' | sudo tee -a /etc/qemu/bridge.conf >/dev/null", "")

	// ── 7. Define VM on target ────────────────────────────────
	setMigStatus("define", 90, "Defining VM on target")
	logStep("defining VM on target")

	xmlTmp := "/tmp/" + vmName + "-migrate.xml"
	if err := os.WriteFile(xmlTmp, []byte(xmlStr), 0644); err != nil {
		return fmt.Errorf("write XML: %w", err)
	}
	defer os.Remove(xmlTmp)

	out, err := sshMigrator(dest, "cat > /tmp/"+vmName+"-define.xml", xmlStr)
	if err != nil {
		return fmt.Errorf("send XML to target: %s", out)
	}
	out, err = sshMigrator(dest, "virsh define /tmp/"+vmName+"-define.xml && rm /tmp/"+vmName+"-define.xml", "")
	if err != nil {
		return fmt.Errorf("virsh define on target: %s", out)
	}

	// ── 8. Start on target ────────────────────────────────────
	setMigStatus("start", 95, "Starting "+vmName+" on target")
	logStep("starting VM on target")
	out, err = sshMigrator(dest, "virsh start "+vmName, "")
	if err != nil {
		return fmt.Errorf("virsh start on target: %s", out)
	}

	// ── 9. Undefine on source ─────────────────────────────────
	setMigStatus("start", 98, "Cleaning up source")
	logStep("undefining VM on source")
	exec.Command("virsh", "undefine", vmName).Run()

	return nil
}

// sshMigrator runs a command on the target via SSH as kvm-migrator.
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
