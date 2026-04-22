package main

// Backup export integration flow (v5.27).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestIntegration_Backup_ExportRoundTrip: admin exports, tarball is
// valid gzip+tar with a manifest and store.json, and the store.json
// is valid JSON matching persistData shape.
func TestIntegration_Backup_ExportRoundTrip(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")

	req, _ := http.NewRequest("POST", h.Server.URL+"/api/admin/backup", nil)
	// Reuse the harness cookie jar by going through its Client.
	resp, err := h.Client.Do(req)
	if err != nil {
		t.Fatalf("backup request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("backup: status=%d body=%s", resp.StatusCode, b)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="kvm-center-backup-`) {
		t.Fatalf("Content-Disposition: %q", cd)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	tr := tar.NewReader(gz)
	var (
		sawManifest, sawStore bool
		storeJSON             []byte
		manifest              backupManifest
	)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		buf := new(bytes.Buffer)
		if _, err := io.Copy(buf, tr); err != nil {
			t.Fatalf("tar read: %v", err)
		}
		switch hdr.Name {
		case "manifest.json":
			sawManifest = true
			if err := json.Unmarshal(buf.Bytes(), &manifest); err != nil {
				t.Fatalf("manifest unmarshal: %v", err)
			}
		case "store.json":
			sawStore = true
			storeJSON = buf.Bytes()
		default:
			t.Errorf("unexpected tar entry %q", hdr.Name)
		}
	}
	if !sawManifest || !sawStore {
		t.Fatalf("missing entries: manifest=%v store=%v", sawManifest, sawStore)
	}
	// v5.32.20 bumped the schema format when tls/ca material joined
	// the tarball. Old backups still restore — restore tooling can
	// read either tag — but fresh exports stamp v2.
	if manifest.SchemaFormat != "persistData.v2" {
		t.Fatalf("schema format: got %q", manifest.SchemaFormat)
	}
	if manifest.CreatedBy != "alice" {
		t.Fatalf("created_by: got %q, want alice", manifest.CreatedBy)
	}
	// store.json must round-trip as persistData.
	var pd persistData
	if err := json.Unmarshal(storeJSON, &pd); err != nil {
		t.Fatalf("store.json not persistData-shaped: %v", err)
	}
	if len(pd.Users) == 0 {
		t.Fatal("persistData.Users empty — expected at least alice")
	}
}

// TestIntegration_Backup_RequiresAdmin: a Viewer gets 403.
func TestIntegration_Backup_RequiresAdmin(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")
	_, _ = h.do("POST", "/api/auth/logout", nil)
	h.seedUser("bob", "bob-password-12", []string{"role_viewer"})
	h.mustLogin("bob", "bob-password-12")

	if st, _ := h.do("POST", "/api/admin/backup", nil); st != http.StatusForbidden {
		t.Fatalf("viewer backup: want 403, got %d", st)
	}
}
