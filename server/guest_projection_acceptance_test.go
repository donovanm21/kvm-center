//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
)

func TestHistoryAcceptanceGuestExpiredProjectionRecovery(t *testing.T) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("current artifact required")
	}
	b := newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	root := t.TempDir()
	s, _ := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	s.mu.Lock()
	s.roles["fixture-role"].Permissions = append(s.roles["fixture-role"].Permissions, "vm.view")
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("guest expiry acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage", "vm.view"})
	if err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err = s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	original := guestFixture(t, true)
	retained := filepath.Join(root, "original-guest.bson")
	if err = os.WriteFile(retained, original, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err = b.db.Collection(mcoGuestData).InsertOne(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	vm := bson.D{{Key: "_id", Value: legacyfixture.VMID}, {Key: "uuid", Value: legacyfixture.VMID}, {Key: "host_id", Value: "synthetic-guest"}, {Key: "name", Value: "synthetic-expired-projection"}, {Key: "state", Value: "stopped"}, {Key: "guestagent", Value: true}, {Key: "guestsource", Value: "kvm-tools"}, {Key: "guesthostname", Value: "stale-projection"}, {Key: "kvmtoolsversion", Value: "old"}}
	if _, err = b.db.Collection(mcoVMs).InsertOne(t.Context(), vm); err != nil {
		t.Fatal(err)
	}
	if _, err = b.db.Collection(mcoHosts).InsertOne(t.Context(), bson.D{{Key: "_id", Value: "synthetic-guest"}, {Key: "id", Value: "synthetic-guest"}, {Key: "status", Value: "offline"}}); err != nil {
		t.Fatal(err)
	}
	p := startAcceptance(t, binary, root, b, token, false)
	verify := func() {
		t.Helper()
		requireInactiveGuestTTL(t, b)
		var list []*models.VM
		var single models.VM
		if err := json.Unmarshal(p.request(t, "GET", "/api/vms", nil, 200), &list); err != nil || len(list) != 1 {
			t.Fatal("list", err)
		}
		if err := json.Unmarshal(p.request(t, "GET", "/api/vms/"+legacyfixture.VMID, nil, 200), &single); err != nil {
			t.Fatal(err)
		}
		for _, v := range []*models.VM{list[0], &single} {
			if v.GuestAgent || v.GuestSource != "" || v.GuestHostname != "" || v.KVMToolsVersion != "" {
				t.Fatal("expired persisted projection resurrected", v)
			}
		}
		var raw bson.Raw
		if err := b.db.Collection(mcoGuestData).FindOne(t.Context(), bson.D{{Key: "_id", Value: legacyfixture.VMID}}).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		// Mongo may reorder envelope keys during restore; compare exact retained nested entry.
		if !bytes.Equal(raw.Lookup("entry").Document(), original.Lookup("entry").Document()) || raw.Lookup("updated_at").DateTime() != original.Lookup("updated_at").DateTime() {
			t.Fatal("expiry modified retained report")
		}
		disk, err := os.ReadFile(retained)
		if err != nil || !bytes.Equal(disk, original) {
			t.Fatal("original modified", err)
		}
	}
	verify()
	p.stop(t, false)
	p = startAcceptance(t, binary, root, b, token, false)
	verify()
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restoredRoot := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restoredRoot, 0)
	if !bytes.Equal(snapshot.EntityOverlay.Collections[mcoGuestData][legacyfixture.VMID], original) {
		t.Fatal("backup original changed")
	}
	b = newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	p = startAcceptance(t, binary, restoredRoot, b, token, false)
	verify()
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	if !bytes.Equal(second.EntityOverlay.Collections[mcoGuestData][legacyfixture.VMID], original) {
		t.Fatal("second backup original changed")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restoredRoot, b, token, false)
	verify()
	p.stop(t, false)
	sum := sha256.Sum256(original)
	emitAcceptance(t, "guest_expired_projection", map[string]any{"backend": "mongo", "fixture_schema": "synthetic-guest-report-bson-v1", "original_sha256": hex.EncodeToString(sum[:]), "expired_list_single_hidden": true, "restart_backup_restore_restart": true, "raw_report_and_timestamps_retained": true, "ttl_monitor_inactive": true, "update_or_grow_dispatched": false, "full_legacy_compatible": false})
}
