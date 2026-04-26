package main

// Periodic backup scheduler (v5.43.0). The backup endpoint and CLI
// have shipped since v5.27 / v5.32.20; this layer ties them to a
// recurring tick so operators don't have to wire their own cron.
//
// Model: a single in-process goroutine wakes once a minute, asks
// the store whether a backup is due, runs `ExportSnapshot` against
// `DestPath`, rotates older files past `RetainCount`, and updates
// the schedule's `LastRunAt` / `LastRunErr` fields. Schedule lives
// in persistData so it survives restarts; defaults to disabled.
//
// Out of scope (intentionally): per-target encryption, S3-style
// remote storage, full cron grammar. The roadmap has those as v2
// items if operators ask.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kvm-center/internal/models"
)

const backupSchedulerTick = 1 * time.Minute

// GetBackupSchedule returns a copy of the current schedule, or nil
// if not configured.
func (s *Store) GetBackupSchedule() *models.BackupSchedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.backupSchedule == nil {
		return nil
	}
	cp := *s.backupSchedule
	return &cp
}

// SetBackupSchedule replaces the schedule. Validation lives in the
// handler. Triggers an AsyncSave so the change survives a restart.
func (s *Store) SetBackupSchedule(sc *models.BackupSchedule) {
	s.mu.Lock()
	s.backupSchedule = sc
	s.mu.Unlock()
	s.AsyncSave()
}

// recordBackupRun stamps the most recent run's outcome. Caller
// supplies the size + error; we update timestamps + persist.
func (s *Store) recordBackupRun(t time.Time, size int64, err error) {
	s.mu.Lock()
	if s.backupSchedule != nil {
		s.backupSchedule.LastRunAt = t.UTC()
		s.backupSchedule.LastRunSize = size
		if err != nil {
			s.backupSchedule.LastRunErr = err.Error()
		} else {
			s.backupSchedule.LastRunErr = ""
		}
		if s.backupSchedule.EveryHours > 0 {
			s.backupSchedule.NextRunAt = t.Add(time.Duration(s.backupSchedule.EveryHours) * time.Hour).UTC()
		}
	}
	s.mu.Unlock()
	s.AsyncSave()
}

// startBackupScheduler launches the once-a-minute tick. Cancel via
// the surrounding context (server's main lifetime).
func (s *Store) startBackupScheduler() {
	go func() {
		t := time.NewTicker(backupSchedulerTick)
		defer t.Stop()
		for range t.C {
			s.runBackupSchedulerTick()
		}
	}()
}

func (s *Store) runBackupSchedulerTick() {
	sc := s.GetBackupSchedule()
	if sc == nil || !sc.Enabled {
		return
	}
	if sc.EveryHours <= 0 || sc.DestPath == "" {
		return
	}
	due := sc.LastRunAt.IsZero() || time.Since(sc.LastRunAt) >= time.Duration(sc.EveryHours)*time.Hour
	if !due {
		return
	}
	now := time.Now().UTC()
	size, err := s.runScheduledBackup(sc, now)
	if err != nil {
		log.Printf("backup-scheduler: run failed: %v", err)
	} else {
		log.Printf("backup-scheduler: wrote %d bytes to %s", size, sc.DestPath)
	}
	s.recordBackupRun(now, size, err)
	if sc.RetainCount > 0 {
		if pruneErr := pruneScheduledBackups(sc.DestPath, sc.RetainCount); pruneErr != nil {
			log.Printf("backup-scheduler: rotation failed: %v", pruneErr)
		}
	}
	auditSystem(s, AuditBackupExport, "backup", map[string]string{
		"trigger":     "scheduled",
		"dest":        sc.DestPath,
		"bytes":       fmt.Sprintf("%d", size),
		"every_hours": fmt.Sprintf("%d", sc.EveryHours),
	}, err == nil, errMsg(err))
}

// runScheduledBackup writes one tarball to DestPath. Returns size
// in bytes on success.
func (s *Store) runScheduledBackup(sc *models.BackupSchedule, ts time.Time) (int64, error) {
	if err := os.MkdirAll(sc.DestPath, 0o700); err != nil {
		return 0, fmt.Errorf("dest mkdir: %w", err)
	}
	storeBytes, err := s.ExportSnapshot()
	if err != nil {
		return 0, fmt.Errorf("export: %w", err)
	}
	entries := []backupEntry{{"store.json", storeBytes, 0o600}}
	if serverBackupPaths.TLSCert != "" {
		if b, err := os.ReadFile(serverBackupPaths.TLSCert); err == nil {
			entries = append(entries, backupEntry{"tls/server.crt", b, 0o644})
		}
	}
	if serverBackupPaths.TLSKey != "" {
		if b, err := os.ReadFile(serverBackupPaths.TLSKey); err == nil {
			entries = append(entries, backupEntry{"tls/server.key", b, 0o600})
		}
	}
	if serverBackupPaths.CADir != "" {
		if caEntries, err := readDirForBackup(serverBackupPaths.CADir, "ca/"); err == nil {
			entries = append(entries, caEntries...)
		}
	}
	manifest := backupManifest{
		Version:      Version,
		CreatedAt:    ts,
		StoreBytes:   int64(len(storeBytes)),
		SchemaFormat: "persistData.v2",
		Files:        make(map[string]string, len(entries)),
		CreatedBy:    "scheduler",
	}
	for _, e := range entries {
		sum := sha256.Sum256(e.data)
		manifest.Files[e.name] = hex.EncodeToString(sum[:])
	}
	manBytes, _ := json.MarshalIndent(&manifest, "", "  ")

	filename := fmt.Sprintf("kvm-center-backup-%s.tar.gz", ts.Format("20060102-150405"))
	full := filepath.Join(sc.DestPath, filename)
	tmp := full + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create tarball: %w", err)
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	writeTarFile := func(name string, data []byte, mode int64) error {
		h := &tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: ts}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := writeTarFile("manifest.json", manBytes, 0o644); err != nil {
		out.Close()
		os.Remove(tmp)
		return 0, fmt.Errorf("write manifest: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for _, e := range entries {
		if err := writeTarFile(e.name, e.data, e.mode); err != nil {
			out.Close()
			os.Remove(tmp)
			return 0, fmt.Errorf("write %s: %w", e.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return 0, fmt.Errorf("close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return 0, fmt.Errorf("close gz: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("close file: %w", err)
	}
	if err := os.Rename(tmp, full); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("rename: %w", err)
	}
	st, _ := os.Stat(full)
	if st != nil {
		return st.Size(), nil
	}
	return 0, nil
}

// pruneScheduledBackups keeps the newest `keep` files matching
// kvm-center-backup-*.tar.gz under dir, deletes the rest.
func pruneScheduledBackups(dir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type bf struct {
		path    string
		modTime time.Time
	}
	var files []bf
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), "kvm-center-backup-") || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, bf{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	if len(files) <= keep {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	for _, f := range files[keep:] {
		if err := os.Remove(f.path); err != nil {
			log.Printf("backup-scheduler: prune %s: %v", f.path, err)
		}
	}
	return nil
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
