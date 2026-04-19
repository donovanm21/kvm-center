package main

// Backup & restore (v5.27). Exports the full persistent store as a
// signed JSON snapshot inside a tar.gz, letting operators capture
// kvm-center's state for disaster recovery or pre-upgrade snapshots.
//
// Scope:
//   - Store JSON (everything Save() writes: users, roles, hosts, VM
//     inventory, hypervisor certs, API tokens, audit log, features,
//     snapshot policies, notifications, metrics config, console secret).
//   - manifest.json with version + timestamp + operator identity.
//
// NOT included — backed up separately via scripts/kvmc-backup:
//   - TLS server cert + key at $KVM_CENTER_TLS_DIR (on-disk file)
//   - Cluster CA material at $KVM_CENTER_CA_DIR (on-disk file)
//   - docker-compose .env (contains KVM_MONGO_PASSWORD)
//
// Restore is a separate CLI workflow (bootstrap.sh --restore) because
// replacing live state in-process is hazardous: migrations in flight,
// open sessions, etc. See server/docs/backup.md.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// backupManifest travels inside every backup tarball and records what
// the archive contains + when it was made + which kvm-center built it.
// Used at restore time to reject incompatible versions and to surface
// provenance in the audit log.
type backupManifest struct {
	Version      string    `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	CreatedByID  string    `json:"created_by_id,omitempty"`
	CreatedBy    string    `json:"created_by,omitempty"`
	StoreBytes   int64     `json:"store_bytes"`
	SchemaFormat string    `json:"schema_format"` // JSONBackend persistData wire format
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

// handleBackup streams a tar.gz of the store JSON + manifest.
// Gated on user.manage at route registration. An audit event with
// the requesting user's ID lets admins track who exported state.
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
		manifest := backupManifest{
			Version:      Version,
			CreatedAt:    time.Now().UTC(),
			StoreBytes:   int64(len(storeBytes)),
			SchemaFormat: "persistData.v1",
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
		if err := writeTarFile("manifest.json", manBytes, 0o644); err != nil {
			return
		}
		if err := writeTarFile("store.json", storeBytes, 0o600); err != nil {
			return
		}

		_ = tw.Close()
		_ = gz.Close()

		actor := ""
		if u != nil {
			actor = u.ID
		}
		audit(store, r, AuditBackupExport, "backup", map[string]string{
			"store_bytes": fmt.Sprintf("%d", manifest.StoreBytes),
			"actor":       actor,
		}, true, "")
	}
}
