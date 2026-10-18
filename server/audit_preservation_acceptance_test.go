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

func TestHistoryAcceptanceAuditLifecycle(t *testing.T) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("current artifact required")
	}
	b := newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	root := t.TempDir()
	s, _ := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	s.mu.Lock()
	s.roles["fixture-role"].Permissions = append(s.roles["fixture-role"].Permissions, "audit.view")
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("audit acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage", "audit.view"})
	if err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	original := auditFixture(t, "synthetic-original-audit")
	originalPath := filepath.Join(root, "original-audit.bson")
	if err := os.WriteFile(originalPath, original, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Collection(mcoAuditEvents).InsertOne(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	verify := func() {
		t.Helper()
		var got bson.Raw
		if err := b.db.Collection(mcoAuditEvents).FindOne(t.Context(), bson.D{{Key: "_id", Value: "synthetic-original-audit"}}).Decode(&got); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"id", "at", "actor_id", "actorname", "actorip", "action", "resource", "success", "error", "opaque"} {
			if !equalRaw(got.Lookup(key), original.Lookup(key)) {
				t.Fatal("retained audit value changed", key)
			}
		}
		disk, err := os.ReadFile(originalPath)
		if err != nil || !bytes.Equal(disk, original) {
			t.Fatal("original changed")
		}
	}
	p := startAcceptance(t, binary, root, b, token, false)
	assertRead := func() {
		t.Helper()
		var response struct {
			Events []*models.AuditEvent `json:"events"`
			Total  int                  `json:"total"`
		}
		raw := p.request(t, "GET", "/api/audit?actor=synthetic-actor&action=legacy.event&resource=RESOURCE&success=false&since=1577836800&until=1609459200&limit=1", nil, 200)
		if err := json.Unmarshal(raw, &response); err != nil || response.Total != 1 || len(response.Events) != 1 || response.Events[0].Detail["note"] != "retained" {
			t.Fatal("legacy audit filter/read failed")
		}
	}
	assertRead()
	verify()
	// Backup export itself is an application mutation that appends an audit
	// event after the archive snapshot; no host or external sink is involved.
	p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	p = startAcceptance(t, binary, root, b, token, false)
	if !bytes.Contains(p.request(t, "GET", "/api/audit?action=backup.export", nil, 200), []byte(`"action":"backup.export"`)) {
		t.Fatal("application append lost after restart")
	}
	assertRead()
	verify()
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restoreRoot := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restoreRoot, 0)
	if snapshot.EntityOverlay.Version != 5 || len(snapshot.EntityOverlay.Collections[mcoAuditEvents]) < 2 {
		t.Fatal("audit backup incomplete")
	}
	b = newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	assertRead()
	verify()
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	if !equalRaw(bson.Raw(second.EntityOverlay.Collections[mcoAuditEvents]["synthetic-original-audit"]).Lookup("opaque"), original.Lookup("opaque")) {
		t.Fatal("second backup lost opaque")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	assertRead()
	verify()
	p.stop(t, false)
	sum := sha256.Sum256(original)
	emitAcceptance(t, "audit_lifecycle", map[string]any{"fixture_schema": "synthetic-audit-events-bson-v1", "backend": "mongo", "read_filter_append_restart": true, "application_backup_fresh_restore": true, "second_backup_restart": true, "opaque_preserved": true, "originals_unchanged": true, "original_sha256": hex.EncodeToString(sum[:]), "ttl_monitor_disabled": true, "full_legacy_compatible": false})
}
