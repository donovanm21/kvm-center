package main

import (
	"context"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// randRead is a thin wrapper so the test variant can stub it.
var randRead = rand.Read

// Offline migration over qemu+tls:// (v5.11.6). Parallel to
// offlineMigrate() which still handles the SSH+scp path; dispatch is
// in startOfflineMigrate. When the operator pins keep_raw=true or
// either host isn't enrolled in the cluster CA, we fall back to the
// SSH path transparently.
//
// Trust model:
//   - All remote commands run over the libvirt remote API at
//     qemu+tls://<dest>/system. Mutual-cert auth; cluster CA trusts
//     both endpoints.
//   - Disk bytes flow through libvirtd's vol-upload stream (same
//     connection, no out-of-band scp).
//   - No kvm-migrator ssh keypair required on the hot path.
//
// Scope in v5.11.6:
//   - Targets the "default" storage pool on the destination. v5.11.7
//     adds an operator-selected pool via the advanced modal.
//   - keep_raw (raw→qcow2→raw round-trip) stays on the legacy path —
//     vol-upload alone can't convert formats mid-stream.

const defaultMTLSPool = "default"

func offlineMigrateMTLS(ctx context.Context, vmName, dest string, opts MTLSOptions) error {
	logStep := func(msg string) { log.Printf("offline-migrate-mtls %s→%s: %s", vmName, dest, msg) }
	uri := fmt.Sprintf("qemu+tls://%s/system", dest)
	pool := opts.TargetPool
	if pool == "" {
		// Operator didn't choose via the advanced modal. Prefer
		// "default", otherwise pick the first active pool reported
		// by libvirt. Keeps the happy path working on installs that
		// ship a non-default pool name (Rocky 9 typically has one
		// called "images").
		pool = pickDestinationPool(ctx, uri)
		if pool == "" {
			return errors.New("no active storage pools on destination — create one or pick explicitly in the Advanced modal")
		}
	}
	finalVMName := opts.NewVMName
	if finalVMName == "" {
		finalVMName = vmName
	}

	// ── State tracking for rollback ────────────────────────────
	wasRunning := false
	vmShutDown := false
	var sourceDiskPaths []string
	var uploadedVols []string // pool/name pairs so rollback can vol-delete
	definedOnTarget := false
	var tmpFiles []string

	rollback := func(reason string) {
		logStep("ROLLBACK: " + reason)
		setMigStatus("failed", 0, "Rolling back: "+reason)
		for _, f := range tmpFiles {
			os.Remove(f)
		}
		if definedOnTarget {
			exec.Command("virsh", "-c", uri, "destroy", finalVMName).Run()
			exec.Command("virsh", "-c", uri, "undefine", finalVMName, "--nvram", "--managed-save", "--snapshots-metadata").Run()
			logStep("ROLLBACK: dest libvirt cleanup issued")
		}
		for _, vol := range uploadedVols {
			exec.Command("virsh", "-c", uri, "vol-delete", "--pool", pool, vol).Run()
		}
		if vmShutDown && wasRunning {
			logStep("ROLLBACK: restarting VM on source")
			if out, err := exec.Command("virsh", "start", vmName).CombinedOutput(); err != nil {
				logStep("ROLLBACK: virsh start failed: " + strings.TrimSpace(string(out)))
			} else {
				logStep("ROLLBACK: source VM restarted")
			}
		}
	}

	// ── 1. Pool sanity check ───────────────────────────────────
	setMigStatus("prepare", 2, "Verifying destination storage pool")
	if out, err := exec.CommandContext(ctx, "virsh", "-c", uri, "pool-info", pool).CombinedOutput(); err != nil {
		return fmt.Errorf("destination pool %q not reachable via %s: %s — install-agent.sh enrolled the host but the 'default' pool is missing. Falling back requires reissuing with transport=ssh", pool, uri, strings.TrimSpace(string(out)))
	}

	// ── 2. Current VM state ───────────────────────────────────
	stateOut, err := exec.Command("virsh", "domstate", vmName).Output()
	if err != nil {
		return fmt.Errorf("domstate: %w", err)
	}
	wasRunning = strings.TrimSpace(string(stateOut)) == "running"

	// ── 3. Shutdown source VM ─────────────────────────────────
	if wasRunning {
		setMigStatus("shutdown", 5, "Gracefully shutting down "+vmName)
		logStep("shutting down VM")
		if out, err := exec.Command("virsh", "shutdown", vmName).CombinedOutput(); err != nil {
			rollback("shutdown failed")
			return fmt.Errorf("shutdown: %s", strings.TrimSpace(string(out)))
		}
		// Wait up to 60s.
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			if err := checkCancel(ctx); err != nil {
				rollback("cancelled during shutdown")
				return err
			}
			s, _ := exec.Command("virsh", "domstate", vmName).Output()
			if strings.TrimSpace(string(s)) == "shut off" {
				vmShutDown = true
				break
			}
		}
		if !vmShutDown {
			logStep("graceful shutdown timed out — forcing destroy")
			exec.Command("virsh", "destroy", vmName).Run()
			vmShutDown = true
		}
	} else {
		vmShutDown = true
	}
	setMigStatus("shutdown", 15, vmName+" is shut off")
	logStep("VM is shut off")

	// ── 4. Dump XML ───────────────────────────────────────────
	xmlData, err := exec.Command("virsh", "dumpxml", "--inactive", vmName).Output()
	if err != nil {
		rollback("dumpxml failed")
		return fmt.Errorf("dumpxml: %w", err)
	}

	// ── 5. Parse disks ───────────────────────────────────────
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

	// ── 6. Convert + vol-upload each disk ─────────────────────
	diskCount := 0
	for _, d := range dom.Disks {
		if d.Device == "disk" && d.Source.File != "" {
			diskCount++
		}
	}
	if diskCount == 0 {
		return errors.New("no writable disks found in domain XML")
	}

	diskIdx := 0
	for _, d := range dom.Disks {
		if d.Device != "disk" || d.Source.File == "" {
			continue
		}
		srcPath := d.Source.File
		sourceDiskPaths = append(sourceDiskPaths, srcPath)
		volName := vmName + "-" + d.Target.Dev + ".qcow2"
		tmpQcow := "/tmp/" + vmName + "-" + d.Target.Dev + ".qcow2"

		convertBase := 15 + (35*diskIdx)/diskCount
		convertEnd := 15 + (35*(diskIdx+1))/diskCount
		setMigStatus("convert", convertBase, fmt.Sprintf("Compressing %s (qemu-img convert -c)", d.Target.Dev))
		logStep(fmt.Sprintf("converting %s → compact qcow2", srcPath))
		start := time.Now()
		if out, err := exec.CommandContext(ctx, "qemu-img", "convert", "-c", "-O", "qcow2", srcPath, tmpQcow).CombinedOutput(); err != nil {
			os.Remove(tmpQcow)
			rollback("qemu-img convert failed for " + d.Target.Dev)
			return fmt.Errorf("qemu-img convert %s: %s", d.Target.Dev, strings.TrimSpace(string(out)))
		}
		tmpFiles = append(tmpFiles, tmpQcow)
		fi, _ := os.Stat(tmpQcow)
		sizeMB := int64(0)
		if fi != nil {
			sizeMB = fi.Size() / (1024 * 1024)
		}
		setMigStatus("convert", convertEnd, fmt.Sprintf("Compressed %s to %d MB in %ds", d.Target.Dev, sizeMB, int(time.Since(start).Seconds())))

		// Delete an existing volume at the target to make room.
		exec.CommandContext(ctx, "virsh", "-c", uri, "vol-delete", "--pool", pool, volName).Run()

		// Create the target volume. Capacity = the domain's virtual
		// size, pulled from the source qcow2 — ensures vol-upload
		// has enough room even though the compressed image is
		// smaller. `qemu-img info --output=json` would be more
		// robust but requires pulling encoding/json for one field,
		// so use the raw --output=size path via `qemu-img measure`.
		virtualBytes, err := sourceVirtualSize(srcPath)
		if err != nil {
			rollback("qemu-img info failed")
			return err
		}
		transferBase := 50 + (35*diskIdx)/diskCount
		setMigStatus("transfer", transferBase, fmt.Sprintf("Creating target volume %s (%d MB virtual)", volName, virtualBytes/(1024*1024)))
		if out, err := exec.CommandContext(ctx, "virsh", "-c", uri, "vol-create-as",
			pool, volName, fmt.Sprintf("%d", virtualBytes), "--format", "qcow2").CombinedOutput(); err != nil {
			rollback("vol-create-as failed for " + d.Target.Dev)
			return fmt.Errorf("vol-create-as: %s", strings.TrimSpace(string(out)))
		}
		uploadedVols = append(uploadedVols, volName)

		// Upload the converted qcow2. vol-upload streams through
		// libvirtd over the same qemu+tls connection.
		logStep(fmt.Sprintf("vol-upload %s → %s (%d MB)", tmpQcow, volName, sizeMB))
		start = time.Now()
		if out, err := exec.CommandContext(ctx, "virsh", "-c", uri, "vol-upload",
			"--pool", pool, volName, tmpQcow).CombinedOutput(); err != nil {
			rollback("vol-upload failed for " + d.Target.Dev)
			return fmt.Errorf("vol-upload %s: %s", d.Target.Dev, strings.TrimSpace(string(out)))
		}
		os.Remove(tmpQcow)
		logStep(fmt.Sprintf("uploaded %s in %ds", d.Target.Dev, int(time.Since(start).Seconds())))
		diskIdx++
	}

	// ── 7. Rewrite XML per advanced-modal options ────────────
	// Rename the VM by swapping <name> — keeps the UUID stable so
	// the UI's per-VM state (notes, guest-telemetry cache, console
	// secret) follows the migration. Regenerating MACs is a safety
	// option for cloning workflows: a fresh random locally-admin
	// MAC per <interface> avoids duplicate-MAC collisions when the
	// source VM stays around.
	xmlMut := string(xmlData)
	if finalVMName != vmName {
		xmlMut = replaceDomainName(xmlMut, finalVMName)
		logStep(fmt.Sprintf("renaming %s → %s on target", vmName, finalVMName))
	}
	if opts.RegenerateMAC {
		newXML, n := regenerateAllMACsCount(xmlMut)
		xmlMut = newXML
		logStep(fmt.Sprintf("regenerated %d MAC address(es)", n))
	}

	// ── 8. Define VM on target ────────────────────────────────
	setMigStatus("define", 88, "Defining VM on "+dest)
	defineCmd := exec.CommandContext(ctx, "virsh", "-c", uri, "define", "/dev/stdin")
	defineCmd.Stdin = strings.NewReader(xmlMut)
	if out, err := defineCmd.CombinedOutput(); err != nil {
		rollback("remote virsh define failed")
		return fmt.Errorf("define on %s: %s", dest, strings.TrimSpace(string(out)))
	}
	definedOnTarget = true

	// ── 9. Start VM on target ─────────────────────────────────
	setMigStatus("start", 92, "Starting "+finalVMName+" on "+dest)
	if out, err := exec.CommandContext(ctx, "virsh", "-c", uri, "start", finalVMName).CombinedOutput(); err != nil {
		rollback("remote virsh start failed")
		return fmt.Errorf("start on %s: %s", dest, strings.TrimSpace(string(out)))
	}

	// ── 10. Verify running on target ──────────────────────────
	setMigStatus("verify", 95, "Verifying "+finalVMName+" is running on "+dest)
	verified := false
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		s, _ := exec.CommandContext(ctx, "virsh", "-c", uri, "domstate", finalVMName).Output()
		if strings.TrimSpace(string(s)) == "running" {
			verified = true
			break
		}
	}
	if !verified {
		rollback("VM did not reach running state on target")
		return errors.New("target VM failed to start")
	}
	logStep("VM verified running on target")

	// ── 10. Cleanup source ────────────────────────────────────
	setMigStatus("cleanup", 98, "Cleaning up source")
	// Remove source domain definition + disk files. Mirror of the
	// SSH path's cleanup so the source side looks identical no
	// matter which transport was used.
	if out, err := exec.Command("virsh", "undefine", vmName,
		"--nvram", "--managed-save", "--snapshots-metadata").CombinedOutput(); err != nil {
		logStep("source undefine warning: " + strings.TrimSpace(string(out)))
	}
	for _, p := range sourceDiskPaths {
		if rerr := os.Remove(p); rerr != nil {
			logStep("source disk remove: " + rerr.Error())
		} else {
			logStep("removed source disk: " + p)
		}
	}
	setMigStatus("complete", 100, "Migration completed successfully")
	return nil
}

// replaceDomainName swaps the first <name>…</name> element's text
// with newName. libvirt's domain XML places the domain name as the
// first top-level child of <domain>, so a string-level swap is both
// correct and safe against more complex inner elements further down.
func replaceDomainName(xmlStr, newName string) string {
	start := strings.Index(xmlStr, "<name>")
	if start < 0 {
		return xmlStr
	}
	end := strings.Index(xmlStr[start:], "</name>")
	if end < 0 {
		return xmlStr
	}
	return xmlStr[:start+len("<name>")] + newName + xmlStr[start+end:]
}

// regenerateAllMACs rewrites every <mac address="..."/> in the
// domain XML with a fresh locally-administered unicast MAC. libvirt
// emits single-quoted attributes from dumpxml but accepts either
// style on define — handle both here so we don't no-op on single-
// quote input (the common case for `virsh dumpxml`).
var macRegex = regexp.MustCompile(`(<mac\s+address\s*=\s*["'])([0-9A-Fa-f:]+)(["']\s*/>)`)

func regenerateAllMACs(xmlStr string) string {
	out, _ := regenerateAllMACsCount(xmlStr)
	return out
}

// regenerateAllMACsCount returns the rewritten XML along with the
// number of <mac/> elements replaced so the caller can log accurate
// counts (zero = probably a regex miss worth flagging).
func regenerateAllMACsCount(xmlStr string) (string, int) {
	n := 0
	out := macRegex.ReplaceAllStringFunc(xmlStr, func(m string) string {
		sub := macRegex.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		n++
		return sub[1] + randomLocalMAC() + sub[3]
	})
	return out, n
}

// randomLocalMAC returns a 52:54:00-prefixed MAC — libvirt's
// conventional KVM prefix — with the trailing three bytes random.
// Keeps the OUI consistent so ARP/DHCP infrastructure that keys off
// the QEMU prefix still matches.
func randomLocalMAC() string {
	var b [3]byte
	_, _ = randRead(b[:])
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", b[0], b[1], b[2])
}

// pickDestinationPool asks the destination libvirtd for its active
// storage pools and returns the best one for receiving a new volume.
// Preference: "default" > first active pool. Empty string when
// nothing matches (caller turns that into a surfaced error).
func pickDestinationPool(ctx context.Context, uri string) string {
	out, err := exec.CommandContext(ctx, "virsh", "-c", uri, "pool-list", "--name", "--state-running").Output()
	if err != nil {
		return ""
	}
	var candidates []string
	for _, line := range strings.Split(string(out), "\n") {
		n := strings.TrimSpace(line)
		if n == "" {
			continue
		}
		if n == "default" {
			return n
		}
		candidates = append(candidates, n)
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

// sourceVirtualSize returns the virtual size in bytes for a qcow2 or
// raw disk image by invoking `qemu-img info --output=json`. Used to
// size the target volume before vol-upload.
func sourceVirtualSize(path string) (uint64, error) {
	out, err := exec.Command("qemu-img", "info", "--output=json", path).Output()
	if err != nil {
		return 0, fmt.Errorf("qemu-img info: %w", err)
	}
	// We only need virtual-size. Parse the line cheaply instead of
	// pulling encoding/json into the migration path — qemu-img's
	// output format is stable and this saves an import.
	const key = `"virtual-size":`
	idx := strings.Index(string(out), key)
	if idx < 0 {
		return 0, errors.New("virtual-size not in qemu-img info output")
	}
	tail := string(out[idx+len(key):])
	end := strings.IndexAny(tail, ",}\n")
	if end < 0 {
		return 0, errors.New("virtual-size value unterminated")
	}
	num := strings.TrimSpace(tail[:end])
	var v uint64
	if _, err := fmt.Sscanf(num, "%d", &v); err != nil {
		return 0, fmt.Errorf("parse virtual-size %q: %w", num, err)
	}
	return v, nil
}
