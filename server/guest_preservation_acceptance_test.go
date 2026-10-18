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

func requireInactiveGuestTTL(t *testing.T, b *MongoBackend) {
	t.Helper()
	var params struct {
		Enabled bool `bson:"ttlMonitorEnabled"`
	}
	if err := b.db.Client().Database("admin").RunCommand(t.Context(), bson.D{{Key: "getParameter", Value: 1}, {Key: "ttlMonitorEnabled", Value: 1}}).Decode(&params); err != nil || params.Enabled {
		t.Fatal("preservation acceptance requires an isolated Mongo with TTL monitor disabled", err)
	}
}

func TestHistoryAcceptanceGuestReportLifecycle(t *testing.T) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("current artifact required")
	}
	b := newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	root := t.TempDir()
	s, _ := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	s.mu.Lock()
	s.groups["fixture-group"] = &models.Group{ID: "fixture-group", Name: "synthetic guest group"}
	s.roles["fixture-role"].Permissions = append(s.roles["fixture-role"].Permissions, "vm.view")
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("guest acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage", "vm.view"})
	if err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	// Two independent expired originals: one can receive a current report, one
	// retains an opaque nested entry unchanged. No timestamp is refreshed at import.
	original := guestFixture(t, false)
	var report entityGuestReport
	bson.Unmarshal(original, &report)
	rawNested := guestFixture(t, true)
	const archived = "22222222-2222-4222-8222-222222222222"
	rawNested = guestArchivedOriginal(t)
	originals := map[string]bson.Raw{legacyfixture.VMID: original, archived: rawNested}
	for id, raw := range originals {
		if err := os.WriteFile(filepath.Join(root, id+".bson"), raw, 0400); err != nil {
			t.Fatal(err)
		}
		if _, err := b.db.Collection(mcoGuestData).InsertOne(t.Context(), raw); err != nil {
			t.Fatal(err)
		}
	}
	for col, doc := range map[string]bson.D{
		mcoHosts: {{Key: "_id", Value: "synthetic-guest"}, {Key: "id", Value: "synthetic-guest"}, {Key: "status", Value: "offline"}},
		mcoVMs:   {{Key: "_id", Value: legacyfixture.VMID}, {Key: "uuid", Value: legacyfixture.VMID}, {Key: "host_id", Value: "synthetic-guest"}, {Key: "name", Value: "synthetic-guest-vm"}, {Key: "state", Value: "stopped"}},
	} {
		if _, err := b.db.Collection(col).InsertOne(t.Context(), doc); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.db.Collection(mcoVMs).InsertOne(t.Context(), bson.D{{Key: "_id", Value: archived}, {Key: "uuid", Value: archived}, {Key: "host_id", Value: "synthetic-guest"}, {Key: "name", Value: "synthetic-opaque-guest-vm"}, {Key: "state", Value: "stopped"}}); err != nil {
		t.Fatal(err)
	}
	p := startAcceptance(t, binary, root, b, token, false)
	var vms []models.VM
	if err := json.Unmarshal(p.request(t, "GET", "/api/vms", nil, 200), &vms); err != nil || len(vms) != 2 || vms[0].GuestAgent || vms[1].GuestAgent {
		t.Fatal("expired report became fresh", err)
	}
	pre := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	if pre.GuestData[legacyfixture.VMID].Payload.Arch != report.Entry.Payload.Arch || !pre.GuestData[legacyfixture.VMID].UpdatedAt.Equal(report.Entry.UpdatedAt) {
		t.Fatal("expired report lost or refreshed before mutation")
	}
	// Refusal must happen before mutating the cached report or its VM projection.
	rejected, _ := json.Marshal(models.GuestTelemetryPayload{VMUUID: archived, Hostname: "must-not-appear"})
	p.request(t, "POST", "/api/guest-telemetry", rejected, 409)
	refused := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	if refused.GuestData[archived].Payload.Hostname == "must-not-appear" || !refused.GuestData[archived].UpdatedAt.Equal(pre.GuestData[archived].UpdatedAt) {
		t.Fatal("refused guest mutated cache")
	}
	if bytes.Contains(p.request(t, "GET", "/api/vms", nil, 200), []byte("must-not-appear")) {
		t.Fatal("refused guest mutated VM projection")
	}
	report.Entry.Payload.Hostname = "synthetic-current-guest"
	body, _ := json.Marshal(report.Entry.Payload)
	reply := p.request(t, "POST", "/api/guest-telemetry", body, 200)
	var result map[string]string
	if err := json.Unmarshal(reply, &result); err != nil || len(result) != 1 || result["status"] != "ok" {
		t.Fatal("unexpected guest instruction", err)
	}
	// The existing guest handler relies on a subsequent store save. Trigger an
	// ordinary harmless entity edit, without creating any agent work.
	p.request(t, "PUT", "/api/groups/fixture-group", []byte(`{"description":"guest persistence checkpoint"}`), 200)
	verify := func() {
		requireInactiveGuestTTL(t, b)
		for id, old := range originals {
			var got bson.Raw
			if err := b.db.Collection(mcoGuestData).FindOne(t.Context(), bson.D{{Key: "_id", Value: id}}).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if !equalRaw(old.Lookup("opaque"), got.Lookup("opaque")) {
				t.Fatal("guest root opaque value changed")
			}
			if id == archived {
				if !equalRaw(old.Lookup("entry"), got.Lookup("entry")) || !equalRaw(old.Lookup("updated_at"), got.Lookup("updated_at")) {
					t.Fatal("expired opaque guest changed")
				}
			} else {
				var actual entityGuestReport
				if err := bson.Unmarshal(got, &actual); err != nil {
					t.Fatal(err)
				}
				if actual.Entry.Payload.Hostname != "synthetic-current-guest" || actual.Entry.Payload.Arch != report.Entry.Payload.Arch || actual.Entry.Payload.GrowResult == nil || *actual.Entry.Payload.GrowResult != *report.Entry.Payload.GrowResult {
					t.Fatal("known guest report fields lost")
				}
			}
			disk, err := os.ReadFile(filepath.Join(root, id+".bson"))
			if err != nil || !bytes.Equal(disk, old) {
				t.Fatal("original changed")
			}
		}
		if !bytes.Contains(p.request(t, "GET", "/api/vms", nil, 200), []byte("synthetic-current-guest")) {
			t.Fatal("fresh guest report not readable")
		}
	}
	// Audit persistence is asynchronous: wait for the exact report before restart.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var got entityGuestReport
		err := b.db.Collection(mcoGuestData).FindOne(t.Context(), bson.D{{Key: "_id", Value: legacyfixture.VMID}}).Decode(&got)
		if err == nil && got.Entry.Payload.Hostname == "synthetic-current-guest" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("guest report was not persisted", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, root, b, token, false)
	verify()
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restoreRoot := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restoreRoot, 0)
	if snapshot.EntityOverlay == nil || snapshot.EntityOverlay.Version != 4 {
		t.Fatal("guest overlay absent")
	}
	b = newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	verify()
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	if len(second.EntityOverlay.Collections[mcoGuestData]) != 2 {
		t.Fatal("second backup lost guest overlay")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	verify()
	p.stop(t, false)
	sums := map[string]string{}
	for id, raw := range originals {
		sum := sha256.Sum256(raw)
		sums[id] = hex.EncodeToString(sum[:])
	}
	emitAcceptance(t, "guest_report_lifecycle", map[string]any{"backend": "mongo", "fixture_schema": "synthetic-guest-report-bson-v1", "ttl_monitor_inactive": true, "expired_originals_not_refreshed": true, "nested_opaque_preserved": true, "opaque_update_refused_before_mutation": true, "current_report_read_write_restart": true, "application_backup_fresh_restore": true, "second_backup_restart": true, "original_checksums": sums, "originals_unchanged": true, "update_or_grow_dispatched": false, "full_legacy_compatible": false, "guest_operational_compatibility": false})
}
