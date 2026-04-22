package main

// Backup & restore (v5.27, completeness pass v5.32.20). Exports the
// full persistent store as a signed JSON snapshot inside a tar.gz,
// plus every piece of on-disk security material the control plane
// owns, letting operators capture kvm-center's state for disaster
// recovery or pre-upgrade snapshots.
//
// What the tarball contains:
//
//   manifest.json          — version + timestamp + operator identity +
//                            manifest checksum of each entry below.
//   store.json             — full persistData (hosts, users, VMs, roles,
//                            audit, hypervisor certs, API tokens,
//                            snapshot policies, notifications, etc).
//   tls/server.crt         — server TLS leaf (auto-generated or operator-
//   tls/server.key           supplied).
//   ca/ca.crt              — cluster CA cert (signs every hypervisor
//   ca/ca.key                leaf; retention plan is 10 years).
//   ca/grant-signer.seed   — Ed25519 seed for migration-grant tokens.
//   ca/grant-signer.pub      Public half for verification.
//
// NOT included:
//   .env (Mongo password) — operator chooses to bundle at restore time.
//   Per-agent consumed-grants log — lives on each hypervisor; rebuilds
//     naturally as new migrations land.
//
// Restore is a separate CLI workflow (bootstrap.sh --restore) because
// replacing live state in-process is hazardous: migrations in flight,
// open sessions, etc. See server/docs/backup.md.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// backupPaths carries the on-disk directories the backup wraps. Wired
// from main so the backup handler doesn't hard-code filesystem layout.
// Tier S #11, v5.32.20.
type backupPaths struct {
	TLSCert string // usually /var/lib/kvm-center/tls/server.crt
	TLSKey  string // usually /var/lib/kvm-center/tls/server.key
	CADir   string // usually /var/lib/kvm-center/ca — we bundle the
	// whole directory so future additions (e.g. trust-store bundles)
	// travel automatically.
}

// serverBackupPaths is set by main at startup. Handlers read it when
// the operator triggers a backup.
var serverBackupPaths backupPaths

// backupEntry is one file destined for the tarball. Shared between
// handleBackup and readDirForBackup so they can compose.
type backupEntry struct {
	name string // path inside the tarball
	data []byte
	mode int64
}

// backupManifest travels inside every backup tarball and records what
// the archive contains + when it was made + which kvm-center built it.
// Used at restore time to reject incompatible versions and to surface
// provenance in the audit log. The Files map is a SHA-256 checksum per
// entry so a restore tool can verify integrity before applying.
type backupManifest struct {
	Version      string            `json:"version"`
	CreatedAt    time.Time         `json:"created_at"`
	CreatedByID  string            `json:"created_by_id,omitempty"`
	CreatedBy    string            `json:"created_by,omitempty"`
	StoreBytes   int64             `json:"store_bytes"`
	SchemaFormat string            `json:"schema_format"` // JSONBackend persistData wire format
	Files        map[string]string `json:"files"`         // tarball-path → hex(SHA-256)
}

// ExportSnapshot serialises the in-memory store to JSON bytes. Same
// shape as JSONBackend writes on disk; a dump from here is a
// drop-in replacement for data.json.
func (s *Store) ExportSnapshot() ([]byte, error) {
	s.mu.RLock()
	d := persistData{
		Hosts:            s.hosts,
		VMs:              s.vms,
		Storage:          s.storage,
		VMNotes:          s.vmNotes,
		GuestData:        s.guestData,
		Migrations:       s.migrations,
		Users:            s.users,
		Groups:           s.groups,
		Roles:            s.roles,
		Sessions:         s.sessions,
		IDPs:             s.idps,
		Features:         &s.features,
		Audit:            s.audit,
		AuditShipping:    s.auditShipping,
		SnapshotPolicies: s.snapshotPolicies,
		Notifications:    s.notifications,
		Metrics:          s.metrics,
		APITokens:        s.apiTokens,
		HypervisorCerts:  s.hypervisorCerts,
		ConsoleSecret:    s.consoleSecret,
	}
	s.mu.RUnlock()
	d.Dirty = computeDirty(&d)
	return json.MarshalIndent(&d, "", "  ")
}

// handleBackup streams a tar.gz of the store JSON + TLS material +
// CA material + manifest. Gated on user.manage at route registration.
func handleBackup(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "GET / POST only")
			return
		}
		u := userFromRequest(r)
		storeBytes, err := store.ExportSnapshot()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "export snapshot: "+err.Error())
			return
		}

		// Collect the on-disk materials. Each entry is (tarball-path,
		// data, mode). We read into memory so the manifest can carry
		// per-file SHA-256 without a second-pass read. Total size is
		// KBs (certs + keys).
		entries := []backupEntry{
			{"store.json", storeBytes, 0o600},
		}
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
			caEntries, err := readDirForBackup(serverBackupPaths.CADir, "ca/")
			if err == nil {
				entries = append(entries, caEntries...)
			}
		}

		manifest := backupManifest{
			Version:      Version,
			CreatedAt:    time.Now().UTC(),
			StoreBytes:   int64(len(storeBytes)),
			SchemaFormat: "persistData.v2",
			Files:        make(map[string]string, len(entries)),
		}
		for _, e := range entries {
			sum := sha256.Sum256(e.data)
			manifest.Files[e.name] = hex.EncodeToString(sum[:])
		}
		if u != nil {
			manifest.CreatedByID = u.ID
			manifest.CreatedBy = u.Username
		}
		manBytes, _ := json.MarshalIndent(&manifest, "", "  ")

		filename := fmt.Sprintf("kvm-center-backup-%s.tar.gz",
			manifest.CreatedAt.Format("20060102-150405"))
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

		gz := gzip.NewWriter(w)
		tw := tar.NewWriter(gz)

		writeTarFile := func(name string, data []byte, mode int64) error {
			h := &tar.Header{
				Name:    name,
				Mode:    mode,
				Size:    int64(len(data)),
				ModTime: manifest.CreatedAt,
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			_, err := tw.Write(data)
			return err
		}
		// Manifest first so a streaming restore tool can read it up front.
		if err := writeTarFile("manifest.json", manBytes, 0o644); err != nil {
			return
		}
		// Then entries in a stable order so the tarball is reproducible
		// for the same input state.
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
		for _, e := range entries {
			if err := writeTarFile(e.name, e.data, e.mode); err != nil {
				return
			}
		}

		_ = tw.Close()
		_ = gz.Close()

		actor := ""
		if u != nil {
			actor = u.ID
		}
		audit(store, r, AuditBackupExport, "backup", map[string]string{
			"store_bytes": fmt.Sprintf("%d", manifest.StoreBytes),
			"files":       fmt.Sprintf("%d", len(entries)+1),
			"actor":       actor,
		}, true, "")
	}
}

// readDirForBackup walks dir non-recursively and returns every regular
// file as a backup entry, prefixing names with tarPrefix (e.g. "ca/").
// Skips dotfiles so accidental editor backups don't land in tarballs.
// A missing directory returns an error; handleBackup treats that as a
// soft condition (fresh install with no CA yet).
func readDirForBackup(dir, tarPrefix string) ([]backupEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]backupEntry, 0, len(entries))
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		nm := ent.Name()
		if len(nm) == 0 || nm[0] == '.' {
			continue
		}
		full := filepath.Join(dir, nm)
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		mode := int64(info.Mode().Perm())
		if mode == 0 {
			mode = 0o600
		}
		out = append(out, backupEntry{tarPrefix + nm, b, mode})
	}
	return out, nil
}
