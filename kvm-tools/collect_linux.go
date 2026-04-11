//go:build linux

package main

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

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
	return formatUptime(secs)
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
		fields := strings.Fields(scanner.Text())
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
