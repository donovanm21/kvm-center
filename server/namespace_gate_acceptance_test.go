//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

func TestHistoryAcceptanceAlarmNamespaceRefusal(t *testing.T) {
	testLegacyFixtureRefusal(t, unsupportedNamespaceFixture(t, "alarms"), "alarm_namespace_refusal", `unsupported alarms`)
}
func TestHistoryAcceptanceAffinityNamespaceRefusal(t *testing.T) {
	testLegacyFixtureRefusal(t, unsupportedNamespaceFixture(t, "affinity_rules"), "affinity_namespace_refusal", `unsupported Mongo collection "affinity_rules"`)
}
func TestHistoryAcceptanceUnknownNamespaceRefusal(t *testing.T) {
	testLegacyFixtureRefusal(t, unsupportedNamespaceFixture(t, "future_feature"), "unknown_namespace_refusal", `unsupported Mongo collection "future_feature"`)
}

func TestHistoryAcceptanceCachedBackupNamespaceRefusal(t *testing.T) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("current artifact required")
	}
	b := newMongoIntegrationBackend(t)
	root := t.TempDir()
	_, token := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	p := startAcceptance(t, binary, root, b, token, false)
	// Establish a successful readable current backup first, on this same process.
	// Backup auditing can queue a save after its response. Use an unsupported
	// namespace that the application never replaces; inserting into a supported
	// collection here would race that save and violate the single-writer contract.
	valid := p.request(t, "GET", "/api/admin/backup", nil, 200)
	unpackAcceptance(t, valid, t.TempDir(), 0)
	original := unsupportedNamespaceFixture(t, "future_feature").Documents["future_feature"]
	if err := os.WriteFile(filepath.Join(root, "original-future-feature.bson"), original, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Collection("future_feature").InsertOne(t.Context(), bson.Raw(original)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		response := p.request(t, "GET", "/api/admin/backup", nil, 500)
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(response, &failure) != nil || !strings.Contains(failure.Error, `unsupported Mongo collection "future_feature"`) || bytes.HasPrefix(response, []byte{0x1f, 0x8b}) {
			t.Fatal("unsupported cached backup returned archive or wrong refusal")
		}
	}
	// The unsupported record must also refuse the final checkpoint. A clean
	// drain would incorrectly claim this now-incompatible store was persisted.
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal refusal shutdown")
	}
	select {
	case err := <-p.done:
		p.stopped = true
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 1 {
			t.Fatal("expected refused checkpoint exit status")
		}
	case <-time.After(25 * time.Second):
		t.Fatal("refusal shutdown exceeded budget")
	}
	p.client.CloseIdleConnections()
	log, err := os.ReadFile(filepath.Join(root, "process.log"))
	if err != nil || !bytes.Contains(log, []byte("shutdown status=incomplete backup_consistent=false error=final persistence:")) || !bytes.Contains(log, []byte(`unsupported Mongo collection "future_feature"`)) {
		t.Fatal("shutdown did not report refused final checkpoint")
	}
	var got bson.Raw
	if err := b.db.Collection("future_feature").FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || !bytes.Equal(original, got) {
		t.Fatal("refusal modified unsupported BSON", err)
	}
	disk, err := os.ReadFile(filepath.Join(root, "original-future-feature.bson"))
	if err != nil || !bytes.Equal(original, disk) {
		t.Fatal("original changed")
	}
	sum := sha256.Sum256(original)
	emitAcceptance(t, "cached_namespace_backup_refusal", map[string]any{"backend": "mongo", "fixture_collection": "future_feature", "concurrent_external_writer_supported": false, "same_process_initial_backup_readable": true, "unsupported_backup_refused": true, "no_partial_archive": true, "final_checkpoint_refused": true, "originals_unchanged": true, "original_sha256": hex.EncodeToString(sum[:]), "active_compatible": false})
}
