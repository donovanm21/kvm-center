package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestHandleBackup_BundlesTLSAndCA: the /api/admin/backup tarball
// includes the configured TLS material, CA directory contents, and
// a manifest whose checksums match every packed file.
func TestHandleBackup_BundlesTLSAndCA(t *testing.T) {
	tmp := t.TempDir()
	tlsCertPath := filepath.Join(tmp, "server.crt")
	tlsKeyPath := filepath.Join(tmp, "server.key")
	caDir := filepath.Join(tmp, "ca")
	_ = os.MkdirAll(caDir, 0o755)
	if err := os.WriteFile(tlsCertPath, []byte("-- fake cert --"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tlsKeyPath, []byte("-- fake key --"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.crt"), []byte("-- ca --"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "ca.key"), []byte("-- cakey --"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caDir, "grant-signer.seed"), []byte("-- seed --"), 0o600); err != nil {
		t.Fatal(err)
	}

	saved := serverBackupPaths
	defer func() { serverBackupPaths = saved }()
	serverBackupPaths = backupPaths{TLSCert: tlsCertPath, TLSKey: tlsKeyPath, CADir: caDir}

	store := NewStore(filepath.Join(tmp, "data.json"))
	// handleBackup triggers an audit event via AsyncSave, which fires a
	// goroutine that writes data.json AFTER this test returns. Wait for
	// it so t.TempDir cleanup doesn't race the save and error out.
	t.Cleanup(func() { store.saveWG.Wait() })
	if err := store.SetVMTags(tagFixtureUUID, []string{"backup-fixture"}); err != nil {
		t.Fatal(err)
	}
	handler := handleBackup(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("handler returned %d: %s", rr.Code, rr.Body.String())
	}

	// Decode tarball.
	gz, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip open: %v", err)
	}
	tr := tar.NewReader(gz)
	got := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, tr); err != nil {
			t.Fatalf("tar body %s: %v", h.Name, err)
		}
		got[h.Name] = buf.Bytes()
	}
	// Required entries.
	for _, want := range []string{"manifest.json", "store.json", "tls/server.crt", "tls/server.key", "ca/ca.crt", "ca/ca.key", "ca/grant-signer.seed"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("backup tarball missing %s (contents: %v)", want, keys(got))
		}
	}
	// Manifest checksums match body bytes.
	var m backupManifest
	if err := json.Unmarshal(got["manifest.json"], &m); err != nil {
		t.Fatalf("manifest json: %v", err)
	}
	if m.FeatureSchemas["vm_tag_state"] != 1 {
		t.Fatal("backup omitted tag schema requirement")
	}
	var snapshot persistData
	if err := json.Unmarshal(got["store.json"], &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.VMTagState == nil || len(snapshot.VMTagState.ByVM[tagFixtureUUID]) != 1 {
		t.Fatal("archive lost tag state")
	}
	for name, want := range m.Files {
		data, ok := got[name]
		if !ok {
			t.Fatalf("manifest references %s but tarball has no such entry", name)
		}
		sum := sha256.Sum256(data)
		gotHex := hex.EncodeToString(sum[:])
		if gotHex != want {
			t.Fatalf("%s checksum mismatch: manifest=%s actual=%s", name, want[:12], gotHex[:12])
		}
	}
	if m.SchemaFormat != "persistData.v2" {
		t.Fatalf("schema_format = %q, want persistData.v2", m.SchemaFormat)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
