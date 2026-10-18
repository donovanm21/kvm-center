//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
	"kvm-center/internal/offlinestaging"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestHistoryAcceptanceActiveLegacyHistory(t *testing.T) {
	binary, offline := os.Getenv("KVMC_HISTORY_BINARY"), os.Getenv("KVMC_OFFLINE_BINARY")
	if binary == "" || offline == "" {
		t.Fatal("built artifacts required")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	fixture := legacyfixture.GenerateHistoryReader()
	if err := legacyfixture.Write(source, fixture); err != nil {
		t.Fatal(err)
	}
	original := fixture.Documents["perf_history"]
	id := bson.Raw(original).Lookup("_id").StringValue()
	for _, name := range []string{"snapshot.json", "perf_history.bson", "perf_history.indexes.bson"} {
		if err := os.Chmod(filepath.Join(source, name), 0400); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		raw, err := exec.CommandContext(ctx, offline, args...).Output()
		if err != nil {
			t.Fatal("conversion artifact", err)
		}
		return raw
	}
	stage := filepath.Join(root, "stage")
	run("-mode", "convert", "-source", source, "-bundle", stage)
	var state legacyHistoryState
	if err := json.Unmarshal(run("-mode", "legacy-history-state", "-bundle", stage), &state); err != nil {
		t.Fatal(err)
	}
	s, token := acceptanceSeed(t, NewJSONBackend(filepath.Join(root, "state.json")), filepath.Join(root, "state.json"), false)
	s.mu.Lock()
	s.legacyHistory = state.Clone()
	s.hosts[id] = &models.Host{ID: id}
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	b := newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	p := startAcceptance(t, binary, root, b, token, true)
	read := func() []byte {
		t.Helper()
		return p.request(t, "GET", "/api/hosts/"+id+"/history?source=legacy", nil, 200)
	}
	first := read()
	var response struct {
		Tiers []offlinestaging.LegacyHistoryTier `json:"tiers"`
	}
	expected, err := state.View(id)
	if err != nil || json.Unmarshal(first, &response) != nil || len(response.Tiers) != 3 || !reflect.DeepEqual(response.Tiers, expected.Tiers) {
		t.Fatal("legacy stored tiers changed")
	}

	if !bytes.Contains(first, []byte(`"read_only":true`)) || !bytes.Contains(first, []byte(fixture.Checksums["perf_history.bson"])) {
		t.Fatal("legacy API contract")
	}
	verify := func() {
		t.Helper()
		if !bytes.Equal(first, read()) {
			t.Fatal("legacy interpretation changed")
		}
		var got bson.Raw
		if err := b.db.Collection(mcoLegacyHistory).FindOne(t.Context(), bson.D{{Key: "_id", Value: id}}).Decode(&got); err != nil || !bytes.Equal(got, original) {
			t.Fatal("raw legacy changed", err)
		}
		disk, err := os.ReadFile(filepath.Join(source, "perf_history.bson"))
		if err != nil || !bytes.Equal(disk, original) {
			t.Fatal("original changed", err)
		}
	}
	current := p.sample(t, id, 42)
	if current.Last == 0 || len(current.Points) == 0 {
		t.Fatal("new writes missing")
	}
	verify()
	p.stop(t, false)
	p = startAcceptance(t, binary, root, b, token, true)
	verify()
	if p.history(t, id).Last != current.Last {
		t.Fatal("new history restart")
	}
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restored := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restored, 1)
	if !reflect.DeepEqual(snapshot.LegacyHistory, &state) {
		t.Fatal("backup changed raw state")
	}
	b = newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	p = startAcceptance(t, binary, restored, b, token, true)
	verify()
	if p.history(t, id).Last != current.Last {
		t.Fatal("new history restore")
	}
	p.sample(t, id, 43)
	verify()
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 1)
	if !reflect.DeepEqual(second.LegacyHistory, &state) {
		t.Fatal("second backup")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restored, b, token, true)
	verify()
	p.stop(t, false)
	emitAcceptance(t, "active_legacy_history_lifecycle", map[string]any{"backend": "mongo", "original_sha256": fixture.Checksums["perf_history.bson"], "immutable_originals": true, "all_retained_tiers": true, "independent_current_writes": true, "restart": true, "application_backup_fresh_restore": true, "second_backup_restart": true, "ttl_monitor_disabled": true, "legacy_aggregation_resumed": false, "full_legacy_compatible": false})
}
