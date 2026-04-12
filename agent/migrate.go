package main

import (
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

const migratorKey = "/var/lib/kvm-migrator/.ssh/id_ed25519"
const migratorUser = "kvm-migrator"
const imageDir = "/var/lib/libvirt/images"

// offlineMigrate shuts down a VM, compresses its disks via qemu-img convert,
// transfers them to the target host, defines and starts the VM there, then
// cleans up the source. Returns a human-readable status message.
func offlineMigrate(vmName, dest string) (string, error) {
	var steps []string
	logStep := func(msg string) {
		log.Printf("offline-migrate %s→%s: %s", vmName, dest, msg)
		steps = append(steps, msg)
	}

	// ── 1. Check source state ─────────────────────────────────
	stateOut, err := exec.Command("virsh", "domstate", vmName).Output()
	if err != nil {
		return "", fmt.Errorf("cannot read VM state: %w", err)
	}
	state := strings.TrimSpace(string(stateOut))

	// ── 2. Shut down if running ───────────────────────────────
	if state == "running" {
		logStep("shutting down VM")
		if out, err := exec.Command("virsh", "shutdown", vmName).CombinedOutput(); err != nil {
			return "", fmt.Errorf("shutdown failed: %s", strings.TrimSpace(string(out)))
		}
		for i := 0; i < 60; i++ {
			time.Sleep(2 * time.Second)
			s, _ := exec.Command("virsh", "domstate", vmName).Output()
			if strings.TrimSpace(string(s)) == "shut off" {
				break
			}
			if i == 59 {
				exec.Command("virsh", "destroy", vmName).Run()
				logStep("force-stopped after 120s timeout")
			}
		}
	}
	logStep("VM is shut off")

	// ── 3. Dump XML ───────────────────────────────────────────
	xmlData, err := exec.Command("virsh", "dumpxml", "--inactive", vmName).Output()
	if err != nil {
		return "", fmt.Errorf("dumpxml failed: %w", err)
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
		XMLName xml.Name   `xml:"domain"`
		Disks   []domDisk  `xml:"devices>disk"`
	}
	var dom domXML
	if err := xml.Unmarshal(xmlData, &dom); err != nil {
		return "", fmt.Errorf("parse XML: %w", err)
	}

	// ── 5. Convert + transfer each disk ───────────────────────
	xmlStr := string(xmlData)
	for _, d := range dom.Disks {
		if d.Device != "disk" || d.Source.File == "" {
			continue
		}
		srcPath := d.Source.File
		flatName := vmName + "-" + d.Target.Dev + ".qcow2"
		tmpPath := "/tmp/" + flatName
		destPath := imageDir + "/" + flatName

		logStep(fmt.Sprintf("compressing %s → %s", srcPath, tmpPath))
		start := time.Now()
		out, err := exec.Command(
			"qemu-img", "convert", "-c", "-O", "qcow2", "-p", srcPath, tmpPath,
		).CombinedOutput()
		if err != nil {
			os.Remove(tmpPath)
			return "", fmt.Errorf("qemu-img convert failed for %s: %s", d.Target.Dev, strings.TrimSpace(string(out)))
		}

		fi, _ := os.Stat(tmpPath)
		sizeMB := int64(0)
		if fi != nil {
			sizeMB = fi.Size() / (1024 * 1024)
		}
		logStep(fmt.Sprintf("compressed to %d MB in %ds", sizeMB, int(time.Since(start).Seconds())))

		logStep(fmt.Sprintf("transferring %s to %s:%s", flatName, dest, destPath))
		start = time.Now()
		out, err = exec.Command(
			"scp", "-i", migratorKey, "-o", "StrictHostKeyChecking=no",
			tmpPath,
			fmt.Sprintf("%s@%s:%s", migratorUser, dest, destPath),
		).CombinedOutput()
		os.Remove(tmpPath)
		if err != nil {
			return "", fmt.Errorf("scp failed for %s: %s", d.Target.Dev, strings.TrimSpace(string(out)))
		}
		logStep(fmt.Sprintf("transferred in %ds", int(time.Since(start).Seconds())))

		// Update XML: replace original path with flat destination path,
		// and remove any backingStore element.
		xmlStr = strings.ReplaceAll(xmlStr, srcPath, destPath)
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

	// ── 6. Define VM on target ────────────────────────────────
	xmlTmp := "/tmp/" + vmName + "-migrate.xml"
	if err := os.WriteFile(xmlTmp, []byte(xmlStr), 0644); err != nil {
		return "", fmt.Errorf("write XML: %w", err)
	}
	defer os.Remove(xmlTmp)

	logStep("defining VM on target")
	out, err := sshMigrator(dest, "cat > /tmp/"+vmName+"-define.xml", xmlStr)
	if err != nil {
		return "", fmt.Errorf("send XML to target: %s", out)
	}
	out, err = sshMigrator(dest, "virsh define /tmp/"+vmName+"-define.xml && rm /tmp/"+vmName+"-define.xml", "")
	if err != nil {
		return "", fmt.Errorf("virsh define on target: %s", out)
	}
	logStep("VM defined on target")

	// ── 7. Start on target ────────────────────────────────────
	logStep("starting VM on target")
	out, err = sshMigrator(dest, "virsh start "+vmName, "")
	if err != nil {
		return "", fmt.Errorf("virsh start on target: %s", out)
	}
	logStep("VM running on target")

	// ── 8. Undefine on source ─────────────────────────────────
	logStep("undefining VM on source")
	exec.Command("virsh", "undefine", vmName).Run()

	return "Offline migration completed: " + strings.Join(steps, " → "), nil
}

// sshMigrator runs a command on the target via SSH as kvm-migrator.
// If stdin is non-empty, it's piped to the command.
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
