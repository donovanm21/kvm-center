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
// v5.45.0 added cron expressions + per-tarball AES-GCM encryption.
// S3 / remote storage still deferred — local + scheduled is the
// 80 % case for homelab + small-team deployments.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"kvm-center/internal/models"
)

const backupSchedulerTick = 1 * time.Minute

// Encrypted-tarball envelope (v5.45.0):
//   "KVMCB1\n" magic    (7 bytes — "KVMC Backup v1\n")
//   16-byte salt        (PBKDF2)
//   12-byte AES-GCM nonce
//   ciphertext + tag    (rest of file)
//
// PBKDF2-SHA256 / 200 000 iters / 32-byte key derived from the
// operator-supplied passphrase. The full plaintext tarball is the
// GCM input — restore strips the envelope, decrypts in one pass,
// then unpacks. Authenticated under GCM so tampering is detected.
const (
	backupEnvelopeMagic = "KVMCB1\n"
	backupSaltLen       = 16
	backupGCMNonceLen   = 12
	backupPBKDF2Iter    = 200_000
	backupAESKeyLen     = 32
)

// cronParser is the standard 5-field parser (no seconds).
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

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
		// Compute next-run target. Cron takes priority when set;
		// otherwise fall back to EveryHours.
		if expr := strings.TrimSpace(s.backupSchedule.CronExpr); expr != "" {
			if sched, perr := cronParser.Parse(expr); perr == nil {
				s.backupSchedule.NextRunAt = sched.Next(t.UTC())
			}
		} else if s.backupSchedule.EveryHours > 0 {
			s.backupSchedule.NextRunAt = t.Add(time.Duration(s.backupSchedule.EveryHours) * time.Hour).UTC()
		}
	}
	s.mu.Unlock()
	s.AsyncSave()
}

// backupIsDue decides whether the scheduler should fire based on
// the configured cadence. Cron expression takes priority. Returns
// (due, err) — invalid schedules report err so the operator sees
// the parse failure in last_run_err.
func backupIsDue(sc *models.BackupSchedule, now time.Time) (bool, error) {
	if sc == nil {
		return false, nil
	}
	expr := strings.TrimSpace(sc.CronExpr)
	if expr != "" {
		sched, err := cronParser.Parse(expr)
		if err != nil {
			return false, fmt.Errorf("cron_expr parse: %w", err)
		}
		// First run after configuration — fire immediately so the
		// operator sees feedback rather than waiting up to a day.
		if sc.LastRunAt.IsZero() {
			return true, nil
		}
		return !sched.Next(sc.LastRunAt).After(now), nil
	}
	if sc.EveryHours <= 0 {
		return false, nil
	}
	if sc.LastRunAt.IsZero() {
		return true, nil
	}
	return time.Since(sc.LastRunAt) >= time.Duration(sc.EveryHours)*time.Hour, nil
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
	if sc.DestPath == "" {
		return
	}
	due, err := backupIsDue(sc, time.Now().UTC())
	if err != nil {
		// Surface invalid cron expressions on the persisted state so
		// the operator sees them in the UI's last-run-error field.
		log.Printf("backup-scheduler: invalid schedule: %v", err)
		s.recordBackupRun(time.Now().UTC(), 0, err)
		return
	}
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

	// Encrypted tarballs land with a `.enc` suffix so a future
	// restore tool can dispatch decryption without sniffing every
	// file. Plain tarballs keep the legacy name.
	suffix := ".tar.gz"
	encrypted := strings.TrimSpace(sc.EncryptionPassphrase) != ""
	if encrypted {
		suffix = ".tar.gz.enc"
	}
	filename := fmt.Sprintf("kvm-center-backup-%s%s", ts.Format("20060102-150405"), suffix)
	full := filepath.Join(sc.DestPath, filename)
	tmp := full + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create tarball: %w", err)
	}
	// When encrypting, build the tarball into a memory buffer first
	// (kilobytes — same size class as backup.go's existing flow) so
	// we can wrap it under AES-GCM in one pass. Plain path streams
	// straight to disk.
	var sink io.Writer = out
	var encBuf *backupEncWriter
	if encrypted {
		encBuf = &backupEncWriter{}
		sink = encBuf
	}
	gz := gzip.NewWriter(sink)
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
	// Encrypted path: take the buffered tarball, wrap in AES-GCM,
	// stream to the output file as `KVMCB1\n<salt><nonce><ct||tag>`.
	if encrypted {
		envelope, encErr := encryptBackupEnvelope(encBuf.buf, []byte(sc.EncryptionPassphrase))
		if encErr != nil {
			out.Close()
			os.Remove(tmp)
			return 0, fmt.Errorf("encrypt: %w", encErr)
		}
		if _, werr := out.Write(envelope); werr != nil {
			out.Close()
			os.Remove(tmp)
			return 0, fmt.Errorf("write envelope: %w", werr)
		}
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

// backupEncWriter buffers a tarball in memory so the scheduler can
// wrap it in one AES-GCM pass after gzip + tar are closed. Sized
// for the typical backup (kilobytes); a giant store grows the
// buffer, which is fine — the tarball already lives in memory in
// `entries` for the manifest hashing.
type backupEncWriter struct {
	buf []byte
}

func (w *backupEncWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

// encryptBackupEnvelope turns a plain tarball into the
// "KVMCB1\n<salt><nonce><ct||tag>" envelope. The passphrase is
// PBKDF2-stretched (200k iterations / SHA-256) to a 32-byte AES
// key, then AES-256-GCM seals the whole tarball.
func encryptBackupEnvelope(plain, pass []byte) ([]byte, error) {
	salt := make([]byte, backupSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("salt: %w", err)
	}
	key := pbkdf2(pass, salt, backupPBKDF2Iter, backupAESKeyLen)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm init: %w", err)
	}
	nonce := make([]byte, backupGCMNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plain, nil)
	out := make([]byte, 0, len(backupEnvelopeMagic)+backupSaltLen+backupGCMNonceLen+len(ct))
	out = append(out, []byte(backupEnvelopeMagic)...)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// DecryptBackupEnvelope is the inverse — used by the restore path
// when it sniffs the magic prefix on an uploaded file. Exported so
// the restore handler can call it without re-implementing.
func DecryptBackupEnvelope(envelope, pass []byte) ([]byte, error) {
	if !strings.HasPrefix(string(envelope[:min(len(envelope), len(backupEnvelopeMagic))]), backupEnvelopeMagic) {
		return nil, fmt.Errorf("not a kvm-center encrypted backup (missing %q magic)", backupEnvelopeMagic)
	}
	headerLen := len(backupEnvelopeMagic) + backupSaltLen + backupGCMNonceLen
	if len(envelope) <= headerLen {
		return nil, fmt.Errorf("truncated envelope (%d bytes)", len(envelope))
	}
	salt := envelope[len(backupEnvelopeMagic) : len(backupEnvelopeMagic)+backupSaltLen]
	nonce := envelope[len(backupEnvelopeMagic)+backupSaltLen : headerLen]
	ct := envelope[headerLen:]
	key := pbkdf2(pass, salt, backupPBKDF2Iter, backupAESKeyLen)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm init: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt (wrong passphrase or tampering): %w", err)
	}
	return plain, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
