package main

// Online restore is intentionally disabled until state and security
// material can be restored transactionally to the active backend. Writing
// data.json while live saves are running can discard the restored state,
// and does not restore an active MongoDB backend.

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// restoreSummary is what /api/admin/restore returns on success.
type restoreSummary struct {
	OK              bool           `json:"ok"`
	Manifest        backupManifest `json:"manifest"`
	FilesRestored   []string       `json:"files_restored"`
	RestartRequired bool           `json:"restart_required"`
	StagedAt        time.Time      `json:"staged_at"`
}

// restorePaths overlays serverBackupPaths so we know where to put
// the unpacked files. The CADir's whole contents go back; tls/
// individual files map to TLSCert / TLSKey; store.json lands
// adjacent to the JSON backend's data file.
type restorePaths struct {
	DataFile string // /var/lib/kvm-center/data.json
	TLSCert  string
	TLSKey   string
	CADir    string
}

var serverRestorePaths restorePaths

// handleRestore fails closed before consuming an upload or modifying any
// live files. Offline recovery requires a stopped service and a verified,
// backend-specific recovery plan; the API must never report false success.
func handleRestore(_ *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		writeError(w, http.StatusServiceUnavailable,
			"Online restore is temporarily unavailable. It requires a backend-aware, transactional restore workflow; no files have been changed. Use the documented offline recovery procedure with the service stopped.")
	}
}

// extractTarGz unpacks gzipped-tar `r` into `dst`. Refuses entries
// that escape `dst` via path traversal.
func extractTarGz(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar header: %w", err)
		}
		// Reject path traversal — tar entries should be relative
		// to dst with no parent-dir hops.
		clean := filepath.Clean(h.Name)
		if strings.HasPrefix(clean, "..") || strings.Contains(clean, "/../") || filepath.IsAbs(clean) {
			return fmt.Errorf("invalid tar entry path: %q", h.Name)
		}
		full := filepath.Join(dst, clean)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(full, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode&0o777))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // size capped by MaxBytesReader
				out.Close()
				return err
			}
			out.Close()
		default:
			// Skip symlinks, devices, etc — tarball is supposed to
			// carry only manifest.json + store.json + cert files.
		}
	}
}

// handleRestart triggers a clean shutdown so docker compose's
// restart policy (or systemd) brings the server back up. The
// operator hits this immediately after a successful restore so
// the new state is loaded.
func handleRestart(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		actor := ""
		if u := userFromRequest(r); u != nil {
			actor = u.ID
		}
		audit(store, r, AuditBackupRestore, "server", map[string]string{
			"action": "restart",
			"actor":  actor,
		}, true, "")
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"message": "Server is restarting; reconnect in a few seconds.",
		})
		// Force a synchronous Save so anything not yet persisted
		// (in-flight migration mutations, audit events) survives
		// the bounce.
		store.Save()
		// Defer the exit so the response makes it back to the
		// client before the process dies. Container's restart
		// policy brings us back.
		goSafe("restore-deferred-exit", func() {
			time.Sleep(500 * time.Millisecond)
			os.Exit(0)
		})
	}
}
