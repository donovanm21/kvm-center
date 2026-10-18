//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestHistoryAcceptanceAlarmNamespaceRefusal(t *testing.T) {
	testLegacyFixtureRefusal(t, unsupportedNamespaceFixture(t, "alarms"), "alarm_namespace_refusal", `unsupported Mongo collection "alarms"`)
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
	valid := p.request(t, "GET", "/api/admin/backup", nil, 200)
	unpackAcceptance(t, valid, t.TempDir(), 0)
	original := unsupportedNamespaceFixture(t, "alarms").Documents["alarms"]
	if err := os.WriteFile(filepath.Join(root, "original-alarms.bson"), original, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Collection("alarms").InsertOne(t.Context(), bson.Raw(original)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		response := p.request(t, "GET", "/api/admin/backup", nil, 500)
		if !bytes.Contains(response, []byte("unsupported Mongo collection")) || bytes.HasPrefix(response, []byte{0x1f, 0x8b}) {
			t.Fatal("unsupported cached backup returned archive or wrong refusal")
		}
	}
	p.stop(t, false)
	var got bson.Raw
	if err := b.db.Collection("alarms").FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || !bytes.Equal(original, got) {
		t.Fatal("refusal modified unsupported BSON", err)
	}
	disk, err := os.ReadFile(filepath.Join(root, "original-alarms.bson"))
	if err != nil || !bytes.Equal(original, disk) {
		t.Fatal("original changed")
	}
	sum := sha256.Sum256(original)
	emitAcceptance(t, "cached_namespace_backup_refusal", map[string]any{"backend": "mongo", "same_process_initial_backup_readable": true, "unsupported_backup_refused": true, "no_partial_archive": true, "originals_unchanged": true, "original_sha256": hex.EncodeToString(sum[:]), "active_compatible": false})
}
