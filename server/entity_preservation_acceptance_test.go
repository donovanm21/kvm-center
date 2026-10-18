//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
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
	s.roles["fixture-role"].Permissions = append(s.roles["fixture-role"].Permissions, "vm.view", "vm.edit")
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("entity acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage", "vm.view", "vm.edit"})
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
		mcoGroups: {{Key: "_id", Value: "fixture-group"}, {Key: "id", Value: "fixture-group"}, {Key: "name", Value: "synthetic group"}, {Key: "roleids", Value: bson.A{"fixture-role"}}, {Key: "opaque", Value: primitive.NewDecimal128(17, 19)}},
		mcoHosts:  {{Key: "_id", Value: host}, {Key: "id", Value: host}, {Key: "status", Value: "offline"}, {Key: "agent_tls_port", Value: int32(7443)}, {Key: "opaque", Value: primitive.NewDecimal128(7, 9)}},
		mcoVMs:    {{Key: "_id", Value: vm}, {Key: "uuid", Value: vm}, {Key: "host_id", Value: host}, {Key: "name", Value: "synthetic-vm"}, {Key: "state", Value: "stopped"}, {Key: "haspassthrough", Value: true}, {Key: "networks", Value: bson.A{bson.D{{Key: "mac", Value: "synthetic-mac"}, {Key: "opaque", Value: primitive.Binary{Subtype: 0x80, Data: []byte{1, 0, 255}}}}}}},
	} {
		raw, err := bson.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		originals[name] = raw
		if err := os.WriteFile(filepath.Join(root, "original-"+name+".bson"), raw, 0400); err != nil {
			t.Fatal(err)
		}
		if _, err := b.db.Collection(name).InsertOne(ctx, bson.Raw(raw)); err != nil {
			t.Fatal(err)
		}
	}
	verify := func() {
		for name, old := range originals {
			var got bson.Raw
			if err := b.db.Collection(name).FindOne(ctx, bson.D{{Key: "_id", Value: old.Lookup("_id")}}).Decode(&got); err != nil {
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
	if !strings.Contains(string(p.request(t, "GET", "/api/vms", nil, 200)), "synthetic-vm") {
		t.Fatal("active VM read failed")
	}
	for _, path := range []string{"/api/users", "/api/groups", "/api/roles"} {
		raw := p.request(t, "GET", path, nil, 200)
		if bytes.Contains(raw, []byte("opaque")) {
			t.Fatal("private overlay exposed through entity API")
		}
	}
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
	second := p.request(t, "GET", "/api/admin/backup", nil, 200)
	secondSnapshot := unpackAcceptance(t, second, t.TempDir(), 0)
	if secondSnapshot.EntityOverlay == nil {
		t.Fatal("second backup lost overlay")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	verify()
	p.stop(t, false)
	sums := map[string]string{}
	for name, raw := range originals {
		h := sha256.Sum256(raw)
		sums[name] = hex.EncodeToString(h[:])
	}
	emitAcceptance(t, "entity_opaque_lifecycle", map[string]any{"backend": "mongo", "fixture_schema": "synthetic-startup-entity-bson-v2", "scope": "host/VM/user/group/role component only; single application writer", "application_backup_fresh_restore": true, "restored_restart": true, "second_backup_overlay": true, "boot_read_write_restart": true, "opaque_raw_values_preserved": true, "original_checksums": sums, "originals_unchanged": true, "full_legacy_compatible": false, "unknown_behavior_supported": false, "legacy_jobs_present": false})
}
