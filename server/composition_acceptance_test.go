//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/x/bsonx/bsoncore"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
	"kvm-center/internal/offlinestaging"
)

func TestHistoryAcceptanceSupportedComposition(t *testing.T) {
	binary, offline := os.Getenv("KVMC_HISTORY_BINARY"), os.Getenv("KVMC_OFFLINE_BINARY")
	if binary == "" || offline == "" {
		t.Fatal("pinned application and converter artifacts required")
	}
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	bootstrap := NewJSONBackend(path)
	seed, _ := acceptanceSeed(t, bootstrap, path, false)
	seed.mu.Lock()
	seed.roles["fixture-role"].Permissions = append(seed.roles["fixture-role"].Permissions, "vm.view", "vm.edit", "host.manage")
	seed.mu.Unlock()
	token, _, err := seed.CreateAPIToken("composition acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage", "vm.view", "vm.edit", "host.manage"})
	if err != nil {
		t.Fatal(err)
	}
	seed.saveWG.Wait()
	if err = seed.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	d, err := bootstrap.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	parts := legacyfixture.CompositionParts()
	originals := map[string][]byte{}
	sums := map[string]string{}
	stages := map[string]string{}
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		raw, e := exec.CommandContext(ctx, offline, args...).CombinedOutput()
		if e != nil {
			t.Fatalf("composition converter failed: %v %s", e, raw)
		}
		return raw
	}
	for part, fixture := range parts {
		src := filepath.Join(root, "original-"+part)
		if err = os.Mkdir(src, 0700); err != nil {
			t.Fatal(err)
		}
		if err = legacyfixture.Write(src, fixture); err != nil {
			t.Fatal(err)
		}
		entries, e := os.ReadDir(src)
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range entries {
			p := filepath.Join(src, entry.Name())
			raw, e := os.ReadFile(p)
			if e != nil {
				t.Fatal(e)
			}
			originals[p] = raw
			sum := sha256.Sum256(raw)
			sums[part+"/"+entry.Name()] = hex.EncodeToString(sum[:])
			if e = os.Chmod(p, 0400); e != nil {
				t.Fatal(e)
			}
		}
		stage := filepath.Join(root, "stage-"+part)
		stages[part] = stage
		run("-mode", "convert", "-source", src, "-bundle", stage)
		run("-mode", "verify", "-bundle", stage)
	}
	// Entity projection comes from the actual converter's separate staging output.
	docs := map[string][]byte{}
	for col := range parts["entities"].Documents {
		raw, e := os.ReadFile(filepath.Join(stages["entities"], "normalized-"+col+".bson"))
		if e != nil {
			t.Fatal(e)
		}
		docs[col] = raw
	}
	compositionEntities(t, d, docs)
	for part, entry := range map[string]struct {
		mode   string
		target any
	}{"history": {"legacy-history-state", &d.LegacyHistory}, "affinity": {"legacy-affinity-state", &d.Affinity}, "tags": {"legacy-tag-state", &d.VMTagState}} {
		if err = json.Unmarshal(run("-mode", entry.mode, "-bundle", stages[part]), entry.target); err != nil {
			t.Fatal(err)
		}
	}
	if err = bootstrap.Save(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	b := newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	verifyOriginals := func() {
		t.Helper()
		for p, want := range originals {
			got, e := os.ReadFile(p)
			if e != nil || !bytes.Equal(got, want) {
				t.Fatal("composition original changed")
			}
		}
		for _, stage := range stages {
			if _, e := offlinestaging.Verify(stage); e != nil {
				t.Fatal(e)
			}
		}
	}
	verifyEntities := func(edited bool) {
		t.Helper()
		for col, all := range docs {
			for len(all) > 0 {
				doc, rest, ok := bsoncore.ReadDocument(all)
				if !ok {
					t.Fatal("bad component BSON")
				}
				all = rest
				old := bson.Raw(doc)
				var got bson.Raw
				if e := b.db.Collection(col).FindOne(t.Context(), bson.D{{Key: "_id", Value: old.Lookup("_id")}}).Decode(&got); e != nil {
					t.Fatal(col, e)
				}
				if !equalRaw(old.Lookup("opaque"), got.Lookup("opaque")) {
					t.Fatalf("composition opaque value lost in %s", col)
				}
				typ, _, e := entitySchema(col)
				if e != nil {
					t.Fatal(e)
				}
				wantModel, gotModel := reflect.New(typ).Interface(), reflect.New(typ).Interface()
				if e = bson.Unmarshal(old, wantModel); e != nil {
					t.Fatal(e)
				}
				if e = bson.Unmarshal(got, gotModel); e != nil {
					t.Fatal(e)
				}
				if edited {
					switch want := wantModel.(type) {
					case *entityVMNote:
						want.Text = "composition durable note"
					case *models.Group:
						want.Description = "composition durable group"
					case *models.Alarm:
						want.Name = "composition durable alarm"
						actual := gotModel.(*models.Alarm)
						if _, e := time.Parse(time.RFC3339, actual.UpdatedAt); e != nil {
							t.Fatal("missing alarm edit timestamp")
						}
						want.UpdatedAt = actual.UpdatedAt
					}
				}
				// BSON includes security fields omitted from public JSON (token hash,
				// revocation and session dates); compare the complete typed record.
				wantWire, e := bson.Marshal(wantModel)
				if e != nil {
					t.Fatal(e)
				}
				gotWire, e := bson.Marshal(gotModel)
				if e != nil {
					t.Fatal(e)
				}
				if !bytes.Equal(wantWire, gotWire) {
					t.Fatalf("composition typed record changed: %s/%s", col, old.Lookup("_id").StringValue())
				}

			}
		}
		verifyOriginals()
		requireInactiveGuestTTL(t, b)
	}
	// Record exact owned runtime index definitions separately from inert originals.
	indexes := func() map[string][]bson.M {
		t.Helper()
		out := map[string][]bson.M{}
		cols := append([]string{}, overlayCollections...)
		cols = append(cols, mcoLegacyHistory, mcoLegacyHistoryContract, "affinity_state", mcoVMTagState)
		for _, col := range cols {
			cur, e := b.db.Collection(col).Indexes().List(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			var raw []bson.M
			if e = cur.All(t.Context(), &raw); e != nil {
				t.Fatal(e)
			}
			for _, v := range raw {
				delete(v, "ns")
				delete(v, "v")
			}
			sort.Slice(raw, func(i, j int) bool { return raw[i]["name"].(string) < raw[j]["name"].(string) })
			out[col] = raw
		}
		return out
	}
	p := startAcceptance(t, binary, root, b, token, false)
	checkReads := func(edited bool) {
		t.Helper()
		affinity, e := loadAffinityState(t.Context(), b.db)
		if e != nil || !reflect.DeepEqual(affinity, d.Affinity) {
			t.Fatal("composed affinity state changed", e)
		}
		historyState, e := loadLegacyHistory(t.Context(), b.db)
		if e != nil || !reflect.DeepEqual(historyState, d.LegacyHistory) {
			t.Fatal("composed history state changed", e)
		}
		tagState, e := loadVMTagState(t.Context(), b.db)
		expectedTags := &vmTagState{Version: d.VMTagState.Version, ByVM: cloneVMTags(d.VMTagState.ByVM)}
		if edited {
			expectedTags.ByVM[legacyfixture.VMID] = []string{"composed", "database"}
		}
		if e != nil || !reflect.DeepEqual(tagState, expectedTags) {
			t.Fatal("composed complete tag state changed", e)
		}
		var vms []models.VM
		if e := json.Unmarshal(p.request(t, "GET", "/api/vms", nil, 200), &vms); e != nil || len(vms) != 3 {
			t.Fatal("composition VM identities", e)
		}
		expectedVMs := map[string]string{legacyfixture.VMID: legacyfixture.HostID, legacyfixture.AffinityVM2: "composition-peer", legacyfixture.AffinityVM3: legacyfixture.HostID}
		for _, v := range vms {
			if host, ok := expectedVMs[v.UUID]; !ok || host != v.HostID {
				t.Fatal("composition VM association changed")
			}
			delete(expectedVMs, v.UUID)
			if v.UUID == legacyfixture.VMID {
				want := "composition original note"
				if edited {
					want = "composition durable note"
				}
				if v.Notes != want {
					t.Fatalf("composition note %q", v.Notes)
				}
				if v.GuestHostname != "" {
					t.Fatal("expired guest data projected as fresh")
				}
			}
		}
		for _, url := range []string{"/api/users", "/api/groups", "/api/roles", "/api/storage", "/api/hosts/retired", "/api/alarms"} {
			raw := p.request(t, "GET", url, nil, 200)
			if bytes.Contains(raw, []byte(`"opaque"`)) {
				t.Fatal("private overlay leaked")
			}
		}
		var history struct {
			Tiers []offlinestaging.LegacyHistoryTier
		}
		raw := p.request(t, "GET", "/api/hosts/"+legacyfixture.HostID+"/history?source=legacy", nil, 200)
		view, e := d.LegacyHistory.View(legacyfixture.HostID)
		if e != nil || json.Unmarshal(raw, &history) != nil || !reflect.DeepEqual(history.Tiers, view.Tiers) {
			t.Fatal("composed legacy tiers changed")
		}
		var tags struct{ Tags []string }
		if json.Unmarshal(p.request(t, "GET", "/api/vms/"+legacyfixture.VMID+"/tags", nil, 200), &tags) != nil {
			t.Fatal("tag read")
		}
		want := []string{"database", "production"}
		if edited {
			want = []string{"composed", "database"}
		}
		if !reflect.DeepEqual(tags.Tags, want) {
			t.Fatal("composition tags changed", tags.Tags)
		}
	}
	checkSnapshot := func(snapshot *persistData) {
		t.Helper()
		if snapshot.EntityOverlay == nil || snapshot.EntityOverlay.Version != 8 || len(snapshot.EntityOverlay.Collections) != 13 {
			t.Fatal("composition backup overlay incomplete")
		}
		if !reflect.DeepEqual(snapshot.LegacyHistory, d.LegacyHistory) || !reflect.DeepEqual(snapshot.Affinity, d.Affinity) {
			t.Fatal("composition converted state changed")
		}
		if snapshot.VMTagState == nil || !reflect.DeepEqual(snapshot.VMTagState.ByVM[legacyfixture.VMID], []string{"composed", "database"}) {
			t.Fatal("composition tag edit lost")
		}
		if snapshot.Groups["fixture-group"].Description != "composition durable group" || len(snapshot.Alarms) != 1 || snapshot.Alarms[0].Name != "composition durable alarm" || snapshot.Alarms[0].Enabled {
			t.Fatal("composition edits lost")
		}
	}
	checkReads(false)
	verifyEntities(false)
	initialIndexes := indexes()
	p.request(t, "POST", "/api/vms/notes", []byte(`{"uuid":"11111111-1111-4111-8111-111111111111","notes":"composition durable note"}`), 200)
	p.request(t, "PUT", "/api/groups/fixture-group", []byte(`{"description":"composition durable group"}`), 200)
	alarms := cloneAlarms(d.Alarms)
	alarms[0].Name = "composition durable alarm"
	body, _ := json.Marshal(alarms)
	p.request(t, "PUT", "/api/alarms", body, 200)
	p.request(t, "PUT", "/api/vms/"+legacyfixture.VMID+"/tags", []byte(`{"tags":["composed","database"],"expected_tags":["database","production"]}`), 200)
	p.stop(t, false)
	p = startAcceptance(t, binary, root, b, token, false)
	checkReads(true)
	verifyEntities(true)
	backup := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restoreRoot := t.TempDir()
	snapshot := unpackAcceptance(t, backup, restoreRoot, 0)
	checkSnapshot(snapshot)
	b = newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	names, e := b.db.ListCollectionNames(t.Context(), bson.D{})
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range names {
		n, e := b.db.Collection(name).CountDocuments(t.Context(), bson.D{})
		if e != nil || n != 0 {
			t.Fatal("restore destination not empty")
		}
	}
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	checkReads(true)
	verifyEntities(true)
	if !reflect.DeepEqual(initialIndexes, indexes()) {
		t.Fatal("runtime index contract changed across restore")
	}
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	checkSnapshot(second)
	if !reflect.DeepEqual(snapshot.VMTagState, second.VMTagState) {
		t.Fatal("tag state changed across fresh restore")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	checkReads(true)
	verifyEntities(true)
	p.stop(t, false)
	accounting, e := legacyfixture.CompositionAccounting()
	if e != nil {
		t.Fatal(e)
	}
	accountingRaw, _ := json.MarshalIndent(accounting, "", "  ")
	accountingRaw = append(accountingRaw, '\n')
	accountingSum := sha256.Sum256(accountingRaw)
	emitAcceptance(t, "supported_composition_lifecycle", map[string]any{"schema": legacyfixture.CompositionSchema, "backend": "mongo", "entity_collections": 13, "converted_components": []string{"deployed-history", "deployed-affinity", "saved-tags"}, "field_index_accounting_sha256": hex.EncodeToString(accountingSum[:]), "original_checksums": sums, "runtime_indexes": initialIndexes, "boot_read_write_restart": true, "application_backup_fresh_restore": true, "second_backup_and_restart": true, "originals_unchanged": true, "opaque_values_preserved": true, "converted_states_preserved": true, "tag_state_preserved": true, "ttl_monitor_disabled": true, "legacy_jobs_present": false, "native_operations_executed": false, "full_arbitrary_schema_compatible": false, "legacy_rollback_verified": false})
}
