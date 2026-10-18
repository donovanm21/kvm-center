//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryAcceptancePassthroughEvidenceJSON(t *testing.T) {
	passthroughEvidenceAcceptance(t, "json")
}
func TestHistoryAcceptancePassthroughEvidenceMongo(t *testing.T) {
	passthroughEvidenceAcceptance(t, "mongo")
}
func passthroughEvidenceAcceptance(t *testing.T, mode string) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("current artifact required")
	}
	root := t.TempDir()
	var b Backend = NewJSONBackend(filepath.Join(root, "state.json"))
	if mode == "mongo" {
		b = newMongoIntegrationBackend(t)
		requireInactiveGuestTTL(t, b.(*MongoBackend))
	}
	s, _ := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	s.mu.Lock()
	s.roles["fixture-role"].Permissions = append(s.roles["fixture-role"].Permissions, "vm.view", "vm.edit")
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("passthrough acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage", "vm.view", "vm.edit"})
	if err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err = s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	original := passthroughFixture(t)
	retained := filepath.Join(root, "original-vm.bson")
	if err = os.WriteFile(retained, original, 0400); err != nil {
		t.Fatal(err)
	}
	d := passthroughSnapshot(t)
	if m, ok := b.(*MongoBackend); ok {
		if _, err = m.db.Collection(mcoHosts).InsertOne(t.Context(), bson.D{{Key: "_id", Value: "fixture"}, {Key: "id", Value: "fixture"}, {Key: "status", Value: "offline"}}); err != nil {
			t.Fatal(err)
		}
		if _, err = m.db.Collection(mcoVMs).InsertOne(t.Context(), original); err != nil {
			t.Fatal(err)
		}
	} else {
		seeded, err := b.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		seeded.VMs = d.VMs
		seeded.Hosts = d.Hosts
		seeded.EntityOverlay = d.EntityOverlay
		if err = b.Save(t.Context(), seeded); err != nil {
			t.Fatal(err)
		}
	}
	p := startAcceptance(t, binary, root, b, token, false)
	endpoint := "/api/vms/" + tagFixtureUUID + "/passthrough-evidence"
	verify := func() {
		t.Helper()
		raw := p.request(t, "GET", endpoint, nil, 200)
		var v passthroughEvidence
		if json.Unmarshal(raw, &v) != nil || v.HasPassthrough == nil || !*v.HasPassthrough || v.NativeVerified || v.PCIRecordsStatus != "present" || len(v.PCIDevices) != 1 || v.PCIDevices[0].SourceAddr != "0000:05:00.0" || bytes.Contains(raw, []byte("opaque")) {
			t.Fatal("passive evidence mismatch")
		}
		disk, err := os.ReadFile(retained)
		if err != nil || !bytes.Equal(disk, original) {
			t.Fatal("original changed", err)
		}
	}
	verify()
	p.request(t, "POST", endpoint, nil, 405)
	telemetry, _ := json.Marshal(models.TelemetryPayload{HostID: "fixture", VMs: []models.VM{*d.VMs["fixture"][0]}})
	p.request(t, "POST", "/api/telemetry", telemetry, 200)
	// This synchronous supported write follows telemetry and establishes its save.
	notes, _ := json.Marshal(map[string]string{"uuid": tagFixtureUUID, "notes": "synthetic passive evidence"})
	p.request(t, "POST", "/api/vms/notes", notes, 200)
	p.stop(t, false)
	p = startAcceptance(t, binary, root, b, token, false)
	verify()
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restored := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restored, 0)
	saved := bson.Raw(snapshot.EntityOverlay.Collections[mcoVMs][tagFixtureUUID])
	for _, key := range []string{"haspassthrough", "pcidevices", "opaque"} {
		if !equalRaw(original.Lookup(key), saved.Lookup(key)) {
			t.Fatal("retained value changed", key)
		}
	}
	if mode == "mongo" {
		b = newMongoIntegrationBackend(t)
		requireInactiveGuestTTL(t, b.(*MongoBackend))
	} else {
		b = NewJSONBackend(filepath.Join(restored, "state.json"))
	}
	p = startAcceptance(t, binary, restored, b, token, false)
	verify()
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	saved = bson.Raw(second.EntityOverlay.Collections[mcoVMs][tagFixtureUUID])
	for _, key := range []string{"haspassthrough", "pcidevices", "opaque"} {
		if !equalRaw(original.Lookup(key), saved.Lookup(key)) {
			t.Fatal("second backup changed", key)
		}
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restored, b, token, false)
	verify()
	p.stop(t, false)
	sum := sha256.Sum256(original)
	emitAcceptance(t, "passthrough_inventory_evidence", map[string]any{"backend": mode, "fixture_schema": "synthetic-passthrough-bson-v1", "original_sha256": hex.EncodeToString(sum[:]), "read_write_restart_backup_restore_restart": true, "omitted_legacy_flag_preserved": true, "opaque_originals_preserved": true, "native_attachment_or_isolation_verified": false, "device_mutation_dispatched": false, "full_legacy_compatible": false})
}
