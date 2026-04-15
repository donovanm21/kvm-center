//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// One PowerShell + CIM call grabs everything we need; results are cached briefly
// so the six read* functions don't each spawn powershell.exe (which costs ~500 ms).
var (
	winCacheMu sync.Mutex
	winCache   *winSnapshot
	winCacheAt time.Time
)

type winSnapshot struct {
	UUID       string  `json:"uuid"`
	OS         string  `json:"os"`
	Kernel     string  `json:"kernel"`
	UptimeSecs float64 `json:"uptime_secs"`
	MemTotalKB uint64  `json:"mem_total_kb"`
	MemFreeKB  uint64  `json:"mem_free_kb"`
	FS         []struct {
		Mountpoint string `json:"mountpoint"`
		FSType     string `json:"fstype"`
		TotalBytes uint64 `json:"total_bytes"`
		FreeBytes  uint64 `json:"free_bytes"`
	} `json:"fs"`
}

const psSnapshotScript = `
$ErrorActionPreference = 'SilentlyContinue'
$os    = Get-CimInstance Win32_OperatingSystem
$prod  = Get-CimInstance Win32_ComputerSystemProduct
$disks = Get-CimInstance Win32_LogicalDisk -Filter "DriveType=3"
$uptimeSecs = 0
if ($os.LastBootUpTime) {
  $uptimeSecs = ((Get-Date) - $os.LastBootUpTime).TotalSeconds
}
$obj = [PSCustomObject]@{
  uuid        = "$($prod.UUID)".ToLower()
  os          = "$($os.Caption)".Trim()
  kernel      = "$($os.Version)"
  uptime_secs = [double]$uptimeSecs
  mem_total_kb = [uint64]$os.TotalVisibleMemorySize
  mem_free_kb  = [uint64]$os.FreePhysicalMemory
  fs = @($disks | ForEach-Object {
    [PSCustomObject]@{
      mountpoint  = "$($_.DeviceID)"
      fstype      = "$($_.FileSystem)"
      total_bytes = [uint64]($_.Size)
      free_bytes  = [uint64]($_.FreeSpace)
    }
  })
}
$obj | ConvertTo-Json -Depth 4 -Compress
`

func ensureWinSnapshot() *winSnapshot {
	winCacheMu.Lock()
	defer winCacheMu.Unlock()
	if winCache != nil && time.Since(winCacheAt) < 5*time.Second {
		return winCache
	}
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psSnapshotScript).Output()
	if err != nil {
		return &winSnapshot{}
	}
	// Compact-JSON output may have a trailing newline; tolerate any whitespace.
	out = []byte(strings.TrimSpace(string(out)))
	var snap winSnapshot
	if err := json.Unmarshal(out, &snap); err != nil {
		return &winSnapshot{}
	}
	winCache = &snap
	winCacheAt = time.Now()
	return winCache
}

func readDMIUUID() (string, error) {
	s := ensureWinSnapshot()
	if s.UUID == "" {
		return "", fmt.Errorf("could not determine DMI UUID via PowerShell/CIM")
	}
	return s.UUID, nil
}

func readOSPretty() string {
	return ensureWinSnapshot().OS
}

func readKernel() string {
	return ensureWinSnapshot().Kernel
}

func readUptime() string {
	return formatUptime(ensureWinSnapshot().UptimeSecs)
}

func readMem() (totalMB, usedMB uint64) {
	s := ensureWinSnapshot()
	if s.MemTotalKB == 0 {
		return 0, 0
	}
	totalMB = s.MemTotalKB / 1024
	if s.MemFreeKB <= s.MemTotalKB {
		usedMB = (s.MemTotalKB - s.MemFreeKB) / 1024
	}
	return
}

func readFilesystems() []GuestFS {
	s := ensureWinSnapshot()
	if len(s.FS) == 0 {
		return nil
	}
	gb := float64(1024 * 1024 * 1024)
	out := make([]GuestFS, 0, len(s.FS))
	for _, fs := range s.FS {
		if fs.TotalBytes == 0 {
			continue
		}
		used := fs.TotalBytes - fs.FreeBytes
		out = append(out, GuestFS{
			Mountpoint: fs.Mountpoint,
			FSType:     fs.FSType,
			TotalGB:    round2(float64(fs.TotalBytes) / gb),
			UsedGB:     round2(float64(used) / gb),
		})
	}
	return out
}
