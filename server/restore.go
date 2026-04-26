package main

// API-driven restore (v5.39.0). Companion to backup.go's
// /api/admin/backup. The CLI path (bootstrap.sh --restore) shipped
// in v5.32.20; this is the in-app equivalent so an operator who
// already has SPA access doesn't need shell on the docker host to
// run a restore.
//
// Flow on the server side:
//   1. Operator uploads a backup tarball via multipart POST.
//   2. Server unpacks to a staging dir, parses manifest.json,
//      verifies SHA-256 of every entry against the manifest.
//   3. Files are copied into the live volumes (data.json, tls/,
//      ca/) — same destination layout as the CLI path.
//   4. Response carries a "restart required" indicator + a summary
//      of what was placed.
//   5. The operator triggers a restart via /api/admin/restart
//      (the SPA's Restore button does this automatically after
//      confirming). Docker's restart policy brings the server
//      back up against the restored data.
//
// We deliberately do NOT hot-swap state in-process — replacing the
// running store while migrations might be in flight, sessions are
// open, and SSE channels are live is hazardous. The restart-after-
// upload pattern matches the CLI's "stop the stack first" hygiene.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	OK              bool      `json:"ok"`
	Manifest        backupManifest `json:"manifest"`
	FilesRestored   []string  `json:"files_restored"`
	RestartRequired bool      `json:"restart_required"`
	StagedAt        time.Time `json:"staged_at"`
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

// handleRestore accepts a multipart upload of a backup tarball and
// applies it to the live volumes. Gated on `user.manage` at route
// registration. The handler enforces a 64 MB upload cap; backup
// tarballs are kilobytes in practice (no VM disks), 64 MB is more
// than enough headroom for a giant audit log.
func handleRestore(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		// Cap upload size before parsing so a malicious peer can't
		// exhaust memory by streaming a multi-GB body.
		r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "parse upload: "+err.Error())
			return
		}
		file, hdr, err := r.FormFile("backup")
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing form field 'backup' (the .tar.gz)")
			return
		}
		defer file.Close()

		stage, err := os.MkdirTemp("", "kvmc-restore-*")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "stage dir: "+err.Error())
			return
		}
		defer os.RemoveAll(stage)

		if err := extractTarGz(file, stage); err != nil {
			writeError(w, http.StatusBadRequest, "extract: "+err.Error())
			return
		}

		manPath := filepath.Join(stage, "manifest.json")
		manBytes, err := os.ReadFile(manPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, "manifest.json missing — is this a v5.32.20+ backup?")
			return
		}
		var manifest backupManifest
		if err := json.Unmarshal(manBytes, &manifest); err != nil {
			writeError(w, http.StatusBadRequest, "invalid manifest.json: "+err.Error())
			return
		}
		if len(manifest.Files) == 0 {
			writeError(w, http.StatusBadRequest, "manifest.json carries no files entry")
			return
		}

		// Verify every file's SHA-256 matches the manifest before we
		// touch live state.
		for rel, want := range manifest.Files {
			full := filepath.Join(stage, rel)
			b, err := os.ReadFile(full)
			if err != nil {
				writeError(w, http.StatusBadRequest, "file missing in tarball: "+rel)
				return
			}
			sum := sha256.Sum256(b)
			if got := hex.EncodeToString(sum[:]); got != want {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("checksum mismatch for %s (want %s..., got %s...)", rel, want[:12], got[:12]))
				return
			}
		}

		// Apply: copy the staged files into the live volumes.
		paths := serverRestorePaths
		var placed []string

		copyTo := func(srcRel, dst string, mode os.FileMode) error {
			src := filepath.Join(stage, srcRel)
			b, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dst, b, mode); err != nil {
				return err
			}
			placed = append(placed, dst)
			return nil
		}

		if _, ok := manifest.Files["store.json"]; ok && paths.DataFile != "" {
			if err := copyTo("store.json", paths.DataFile, 0o600); err != nil {
				writeError(w, http.StatusInternalServerError, "place store.json: "+err.Error())
				return
			}
		}
		if _, ok := manifest.Files["tls/server.crt"]; ok && paths.TLSCert != "" {
			if err := copyTo("tls/server.crt", paths.TLSCert, 0o644); err != nil {
				writeError(w, http.StatusInternalServerError, "place tls cert: "+err.Error())
				return
			}
		}
		if _, ok := manifest.Files["tls/server.key"]; ok && paths.TLSKey != "" {
			if err := copyTo("tls/server.key", paths.TLSKey, 0o600); err != nil {
				writeError(w, http.StatusInternalServerError, "place tls key: "+err.Error())
				return
			}
		}
		// CA dir: copy every staged ca/* file across.
		if paths.CADir != "" {
			for rel := range manifest.Files {
				if !strings.HasPrefix(rel, "ca/") {
					continue
				}
				dst := filepath.Join(paths.CADir, strings.TrimPrefix(rel, "ca/"))
				mode := os.FileMode(0o600)
				if strings.HasSuffix(rel, ".crt") || strings.HasSuffix(rel, ".pub") {
					mode = 0o644
				}
				if err := copyTo(rel, dst, mode); err != nil {
					writeError(w, http.StatusInternalServerError, "place "+rel+": "+err.Error())
					return
				}
			}
		}

		summary := restoreSummary{
			OK:              true,
			Manifest:        manifest,
			FilesRestored:   placed,
			RestartRequired: true,
			StagedAt:        time.Now().UTC(),
		}

		actor := ""
		if u := userFromRequest(r); u != nil {
			actor = u.ID
		}
		audit(store, r, AuditBackupRestore, "backup", map[string]string{
			"upload":      hdr.Filename,
			"files":       fmt.Sprintf("%d", len(placed)),
			"actor":       actor,
			"manifest_v":  manifest.Version,
		}, true, "")

		writeJSON(w, http.StatusOK, summary)
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
		go func() {
			time.Sleep(500 * time.Millisecond)
			os.Exit(0)
		}()
	}
}
