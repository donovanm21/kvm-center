//go:build integration && historyacceptance && linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"kvm-center/internal/models"
)

// An independent component fixture, not a full legacy snapshot with feature
// collections removed. There are no legacy schedules, jobs, sessions or TTL
// inputs here. Full-schema legacy fixtures remain separate refusal gates.
func TestHistoryAcceptanceEntityOpaqueLifecycle(t *testing.T) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("current artifact required")
	}
	b := newMongoIntegrationBackend(t)
	root := t.TempDir()
	s, _ := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	s.mu.Lock()
	s.roles["fixture-role"].Permissions = append(s.roles["fixture-role"].Permissions, "vm.view", "vm.edit", "host.manage")
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("entity acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage", "vm.view", "vm.edit", "host.manage"})
	if err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const host = "synthetic-component"
	const vm = "11111111-1111-4111-8111-111111111111"
	originals := map[string]bson.Raw{}
	collectionName := func(name string) string {
		if name == "removed_hosts_deleted" {
			return mcoRemovedHosts
		}
		return name
	}
	for col, id := range map[string]string{mcoUsers: "fixture-reader", mcoRoles: "fixture-role"} {
		if _, err := b.db.Collection(col).UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, bson.D{{Key: "$set", Value: bson.D{{Key: "opaque", Value: primitive.NewDecimal128(11, 13)}}}}); err != nil {
			t.Fatal(err)
		}
		var raw bson.Raw
		if err := b.db.Collection(col).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		originals[col] = raw
		if err := os.WriteFile(filepath.Join(root, "original-"+col+".bson"), raw, 0400); err != nil {
			t.Fatal(err)
		}
	}
	for name, doc := range map[string]bson.D{
		mcoRemovedHosts:         {{Key: "_id", Value: "Synthetic-Retired.example.invalid"}, {Key: "hostid", Value: "Synthetic-Retired.example.invalid"}, {Key: "removedat", Value: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}, {Key: "removedby", Value: "fixture-reader"}, {Key: "reason", Value: "synthetic retained retirement"}, {Key: "opaque", Value: primitive.NewDecimal128(41, 43)}},
		"removed_hosts_deleted": {{Key: "_id", Value: "synthetic-retired-delete"}, {Key: "hostid", Value: "synthetic-retired-delete"}, {Key: "removedat", Value: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}, {Key: "removedby", Value: "fixture-reader"}, {Key: "reason", Value: "synthetic deletion retirement"}, {Key: "opaque", Value: primitive.NewDecimal128(47, 53)}},
		mcoStorage:              {{Key: "_id", Value: host + "|SyntheticPool"}, {Key: "hostid", Value: host}, {Key: "name", Value: "SyntheticPool"}, {Key: "capacitygb", Value: float64(100)}, {Key: "opaque", Value: primitive.NewDecimal128(23, 29)}},
		mcoVMNotes:              {{Key: "_id", Value: vm}, {Key: "vm_uuid", Value: vm}, {Key: "notes", Value: "original passive note"}, {Key: "opaque", Value: primitive.NewDecimal128(31, 37)}},
		mcoGroups:               {{Key: "_id", Value: "fixture-group"}, {Key: "id", Value: "fixture-group"}, {Key: "name", Value: "synthetic group"}, {Key: "roleids", Value: bson.A{"fixture-role"}}, {Key: "opaque", Value: primitive.NewDecimal128(17, 19)}},
		mcoHosts:                {{Key: "_id", Value: host}, {Key: "id", Value: host}, {Key: "status", Value: "offline"}, {Key: "agent_tls_port", Value: int32(7443)}, {Key: "opaque", Value: primitive.NewDecimal128(7, 9)}},
		mcoVMs:                  {{Key: "_id", Value: vm}, {Key: "uuid", Value: vm}, {Key: "host_id", Value: host}, {Key: "name", Value: "synthetic-vm"}, {Key: "state", Value: "stopped"}, {Key: "haspassthrough", Value: true}, {Key: "networks", Value: bson.A{bson.D{{Key: "mac", Value: "synthetic-mac"}, {Key: "opaque", Value: primitive.Binary{Subtype: 0x80, Data: []byte{1, 0, 255}}}}}}},
	} {
		raw, err := bson.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		originals[name] = raw
		if err := os.WriteFile(filepath.Join(root, "original-"+name+".bson"), raw, 0400); err != nil {
			t.Fatal(err)
		}
		if _, err := b.db.Collection(collectionName(name)).InsertOne(ctx, bson.Raw(raw)); err != nil {
			t.Fatal(err)
		}
	}
	verify := func() {
		for name, old := range originals {
			var got bson.Raw
			if err := b.db.Collection(collectionName(name)).FindOne(ctx, bson.D{{Key: "_id", Value: old.Lookup("_id")}}).Decode(&got); err != nil {
				t.Fatal(err)
			}
			keys := []string{"opaque"}
			if name == mcoHosts {
				keys = []string{"agent_tls_port", "opaque"}
			}
			if name == mcoVMs {
				keys = []string{"haspassthrough", "networks"}
			}
			for _, key := range keys {
				if !equalRaw(old.Lookup(key), got.Lookup(key)) {
					t.Fatalf("opaque field changed: %s.%s", name, key)
				}
			}
			raw, err := os.ReadFile(filepath.Join(root, "original-"+name+".bson"))
			if err != nil || !bytes.Equal(raw, old) {
				t.Fatal("original fixture changed")
			}
		}
	}
	p := startAcceptance(t, binary, root, b, token, false)
	assertRetired := func() {
		raw := p.request(t, "GET", "/api/hosts/retired", nil, 200)
		if !bytes.Contains(raw, []byte("Synthetic-Retired.example.invalid")) || bytes.Contains(raw, []byte("opaque")) {
			t.Fatal("retirement read lost identity or exposed overlay")
		}
		p.request(t, "POST", "/api/telemetry", []byte(`{"host_id":"synthetic-retired.example.invalid"}`), 410)
		if n, err := b.db.Collection(mcoHosts).CountDocuments(ctx, bson.D{{Key: "_id", Value: "synthetic-retired.example.invalid"}}); err != nil || n != 0 {
			t.Fatal("blocked telemetry created inventory", err)
		}
	}
	assertRetired()
	if !strings.Contains(string(p.request(t, "GET", "/api/vms", nil, 200)), "synthetic-vm") {
		t.Fatal("active VM read failed")
	}
	for _, path := range []string{"/api/users", "/api/groups", "/api/roles"} {
		raw := p.request(t, "GET", path, nil, 200)
		if bytes.Contains(raw, []byte("opaque")) {
			t.Fatal("private overlay exposed through entity API")
		}
	}
	var pools []models.StoragePool
	if err := json.Unmarshal(p.request(t, "GET", "/api/storage", nil, 200), &pools); err != nil || len(pools) != 1 || pools[0].Name != "SyntheticPool" {
		t.Fatal("passive storage read failed", err)
	}
	var vms []models.VM
	if err := json.Unmarshal(p.request(t, "GET", "/api/vms", nil, 200), &vms); err != nil || len(vms) != 1 {
		t.Fatal("VM read failed", err)
	}
	pools[0].AllocationGB = 25
	telemetry, _ := json.Marshal(models.TelemetryPayload{HostID: host, VMs: vms, Storage: pools})
	p.request(t, "POST", "/api/telemetry", telemetry, 200)
	p.request(t, "PUT", "/api/groups/fixture-group", []byte(`{"description":"durable startup entity edit"}`), 200)
	body, _ := json.Marshal(map[string]string{"uuid": vm, "notes": "synthetic durable note"})
	p.request(t, "POST", "/api/vms/notes", body, 200)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var got bson.Raw
		err := b.db.Collection(mcoVMs).FindOne(ctx, bson.D{}).Decode(&got)
		if err == nil && got.Lookup("notes").Type != 0 && got.Lookup("notes").StringValue() == "synthetic durable note" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("API write was not durable")
		}
		time.Sleep(50 * time.Millisecond)
	}
	verify()
	p.stop(t, false)
	p = startAcceptance(t, binary, root, b, token, false)
	if !strings.Contains(string(p.request(t, "GET", "/api/vms", nil, 200)), "synthetic durable note") {
		t.Fatal("API write lost after restart")
	}
	verify()
	if !bytes.Contains(p.request(t, "GET", "/api/groups", nil, 200), []byte("durable startup entity edit")) {
		t.Fatal("startup entity edit lost")
	}
	assertStorage := func() {
		var pools []models.StoragePool
		if err := json.Unmarshal(p.request(t, "GET", "/api/storage", nil, 200), &pools); err != nil || len(pools) != 1 || pools[0].AllocationGB != 25 {
			t.Fatal("storage edit lost", err)
		}
	}
	assertStorage()
	assertRetired()
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restoreRoot := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restoreRoot, 0)
	if snapshot.EntityOverlay == nil {
		t.Fatal("backup omitted opaque entity data")
	}
	b = newMongoIntegrationBackend(t)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	if !bytes.Contains(p.request(t, "GET", "/api/vms", nil, 200), []byte("synthetic durable note")) || !bytes.Contains(p.request(t, "GET", "/api/groups", nil, 200), []byte("durable startup entity edit")) {
		t.Fatal("known edits lost on fresh restore")
	}
	verify()
	assertStorage()
	assertRetired()
	second := p.request(t, "GET", "/api/admin/backup", nil, 200)
	secondSnapshot := unpackAcceptance(t, second, t.TempDir(), 0)
	if secondSnapshot.EntityOverlay == nil {
		t.Fatal("second backup lost overlay")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	verify()
	assertStorage()
	assertRetired()
	p.request(t, "POST", "/api/hosts/synthetic-retired-delete/unretire", nil, 200)
	deadline = time.Now().Add(10 * time.Second)
	for {
		n, err := b.db.Collection(mcoRemovedHosts).CountDocuments(ctx, bson.D{{Key: "_id", Value: "synthetic-retired-delete"}})
		if err == nil && n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unretire not durable", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	if bytes.Contains(p.request(t, "GET", "/api/hosts/retired", nil, 200), []byte("synthetic-retired-delete")) {
		t.Fatal("deleted retirement resurrected")
	}
	assertRetired()
	finalSnapshot := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	if _, ok := finalSnapshot.EntityOverlay.Collections[mcoRemovedHosts]["synthetic-retired-delete"]; ok {
		t.Fatal("deleted retirement retained in backup overlay")
	}
	p.stop(t, false)
	sums := map[string]string{}
	for name, raw := range originals {
		h := sha256.Sum256(raw)
		sums[name] = hex.EncodeToString(h[:])
	}
	emitAcceptance(t, "entity_opaque_lifecycle", map[string]any{"backend": "mongo", "fixture_schema": "synthetic-retirement-entity-bson-v4", "scope": "host/VM/user/group/role/storage/notes/retirement component only; single application writer", "retired_telemetry_refused": true, "unretire_durable": true, "application_backup_fresh_restore": true, "restored_restart": true, "second_backup_overlay": true, "boot_read_write_restart": true, "opaque_raw_values_preserved": true, "original_checksums": sums, "originals_unchanged": true, "full_legacy_compatible": false, "unknown_behavior_supported": false, "legacy_jobs_present": false})
}

// Build original v1 wire bytes without invoking the current upgrading marshaler.
func TestHistoryAcceptanceOverlayV1ArchiveRestore(t *testing.T) { priorOverlayArchiveRestore(t, 1) }
func TestHistoryAcceptanceOverlayV2ArchiveRestore(t *testing.T) { priorOverlayArchiveRestore(t, 2) }
func TestHistoryAcceptanceOverlayV3ArchiveRestore(t *testing.T) { priorOverlayArchiveRestore(t, 3) }
func TestHistoryAcceptanceOverlayV4ArchiveRestore(t *testing.T) { priorOverlayArchiveRestore(t, 4) }
func TestHistoryAcceptanceOverlayV5ArchiveRestore(t *testing.T) { priorOverlayArchiveRestore(t, 5) }
func TestHistoryAcceptanceOverlayV6ArchiveRestore(t *testing.T) { priorOverlayArchiveRestore(t, 6) }
func TestHistoryAcceptanceOverlayV7ArchiveRestore(t *testing.T) { priorOverlayArchiveRestore(t, 7) }
func priorOverlayArchiveRestore(t *testing.T, version int) {
	d := overlayFixture(t)
	d.EntityOverlay.Version = version
	if version >= 2 {
		raw, _ := bson.Marshal(bson.D{{Key: "_id", Value: "prior|Pool"}, {Key: "hostid", Value: "prior"}, {Key: "name", Value: "Pool"}, {Key: "opaque", Value: primitive.NewDecimal128(59, 61)}})
		d.Storage = map[string][]*models.StoragePool{"prior": {{HostID: "prior", Name: "Pool"}}}
		d.EntityOverlay.Collections[mcoStorage] = map[string][]byte{"prior|Pool": raw}
	}
	if version >= 3 {
		raw, _ := bson.Marshal(bson.D{{Key: "_id", Value: "prior-retired"}, {Key: "hostid", Value: "prior-retired"}, {Key: "opaque", Value: primitive.NewDecimal128(67, 71)}})
		d.RemovedHosts = map[string]*models.RemovedHost{"prior-retired": {HostID: "prior-retired"}}
		d.EntityOverlay.Collections[mcoRemovedHosts] = map[string][]byte{"prior-retired": raw}
	}
	if version >= 4 {
		raw := guestFixture(t, false)
		var report entityGuestReport
		if err := bson.Unmarshal(raw, &report); err != nil {
			t.Fatal(err)
		}
		d.GuestData = map[string]*guestEntry{report.UUID: &report.Entry}
		d.EntityOverlay.Collections[mcoGuestData] = map[string][]byte{report.UUID: raw}
	}
	if version >= 5 {
		raw := auditFixture(t, "prior-audit")
		var ev models.AuditEvent
		if err := bson.Unmarshal(raw, &ev); err != nil {
			t.Fatal(err)
		}
		d.Audit = []*models.AuditEvent{&ev}
		d.EntityOverlay.Collections[mcoAuditEvents] = map[string][]byte{ev.ID: raw}
	}
	if version >= 6 {
		raw, _ := tokenFixture(t, "12345678")
		var token models.APIToken
		if err := bson.Unmarshal(raw, &token); err != nil {
			t.Fatal(err)
		}
		d.APITokens = map[string]*models.APIToken{token.ID: &token}
		d.EntityOverlay.Collections[mcoAPITokens] = map[string][]byte{token.ID: raw}
	}
	if version >= 7 {
		raw := sessionFixture(t, "prior-session")
		var session models.Session
		if err := bson.Unmarshal(raw, &session); err != nil {
			t.Fatal(err)
		}
		d.Sessions = map[string]*models.Session{session.Token: &session}
		d.EntityOverlay.Collections[mcoSessions] = map[string][]byte{session.Token: raw}
	}
	raw, err := json.Marshal(struct {
		Sessions     map[string]*models.Session       `json:"sessions,omitempty"`
		APITokens    map[string]*models.APIToken      `json:"api_tokens,omitempty"`
		Audit        []*models.AuditEvent             `json:"audit,omitempty"`
		GuestData    map[string]*guestEntry           `json:"guest_data,omitempty"`
		RemovedHosts map[string]*models.RemovedHost   `json:"removed_hosts,omitempty"`
		Groups       map[string]*models.Group         `json:"groups"`
		Storage      map[string][]*models.StoragePool `json:"storage,omitempty"`
		Overlay      *entityOverlay                   `json:"entity_bson_overlay"`
	}{d.Sessions, d.APITokens, d.Audit, d.GuestData, d.RemovedHosts, d.Groups, d.Storage, d.EntityOverlay})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	manifest, _ := json.Marshal(backupManifest{FeatureSchemas: map[string]int{"entity_bson_overlay": version}, Files: map[string]string{"store.json": hex.EncodeToString(sum[:])}})
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range map[string][]byte{"store.json": raw, "manifest.json": manifest} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := unpackAcceptance(t, buf.Bytes(), t.TempDir(), 0)
	b := newMongoIntegrationBackend(t)
	if err := b.restoreEntitySnapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	var got bson.Raw
	if err := b.db.Collection(mcoGroups).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Lookup("name").StringValue() != "edited" || got.Lookup("opaque").Type == 0 {
		t.Fatal("v1 archive restore lost values")
	}
	if version >= 2 {
		var pool bson.Raw
		if err := b.db.Collection(mcoStorage).FindOne(t.Context(), bson.D{}).Decode(&pool); err != nil {
			t.Fatal(err)
		}
		if pool.Lookup("opaque").Type == 0 {
			t.Fatal("v2 archive lost storage overlay")
		}
	}
	if version >= 3 {
		var retired bson.Raw
		if err := b.db.Collection(mcoRemovedHosts).FindOne(t.Context(), bson.D{}).Decode(&retired); err != nil || retired.Lookup("opaque").Type == 0 {
			t.Fatal("v3 retirement lost", err)
		}
	}
	if version >= 4 {
		var got bson.Raw
		if err := b.db.Collection(mcoGuestData).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || got.Lookup("opaque").Type == 0 {
			t.Fatal("v4 guest overlay lost", err)
		}
	}
	if version >= 5 {
		var got bson.Raw
		if err := b.db.Collection(mcoAuditEvents).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || got.Lookup("opaque").Type == 0 {
			t.Fatal("v5 audit overlay lost", err)
		}
	}
	if version >= 6 {
		var got bson.Raw
		if err := b.db.Collection(mcoAPITokens).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || got.Lookup("opaque").Type == 0 {
			t.Fatal("v6 token overlay lost", err)
		}
	}
	if version >= 7 {
		var got bson.Raw
		if err := b.db.Collection(mcoSessions).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || got.Lookup("opaque").Type == 0 {
			t.Fatal("v7 session overlay lost", err)
		}
	}
	emitAcceptance(t, fmt.Sprintf("overlay_v%d_archive_restore", version), map[string]any{"backend": "mongo", "original_wire_version": version, "validated_manifest_version": version, "upgraded_version": 8, "fresh_restore": true, "opaque_preserved": true})
}
