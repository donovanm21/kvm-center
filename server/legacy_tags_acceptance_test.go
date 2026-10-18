//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestHistoryAcceptanceLegacyTagsJSON(t *testing.T)  { legacyTagsAcceptance(t, "json") }
func TestHistoryAcceptanceLegacyTagsMongo(t *testing.T) { legacyTagsAcceptance(t, "mongo") }
func legacyTagsAcceptance(t *testing.T, mode string) {
	binary, offline := os.Getenv("KVMC_HISTORY_BINARY"), os.Getenv("KVMC_OFFLINE_BINARY")
	if binary == "" || offline == "" {
		t.Fatal("built artifacts required")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	fixture := legacyfixture.GenerateTags()
	if err := legacyfixture.Write(source, fixture); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"snapshot.json", "vm_tags.bson", "vm_tags.indexes.bson"} {
		if err := os.Chmod(filepath.Join(source, name), 0400); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, offline, args...).Output()
		if err != nil {
			t.Fatal("offline artifact", err)
		}
		return out
	}
	stage := filepath.Join(root, "stage")
	run("-mode", "convert", "-source", source, "-bundle", stage)
	exported := run("-mode", "legacy-tag-state", "-bundle", stage)
	var state vmTagState
	if err := json.Unmarshal(exported, &state); err != nil {
		t.Fatal(err)
	}
	s, _ := acceptanceSeed(t, NewJSONBackend(filepath.Join(root, "state.json")), filepath.Join(root, "state.json"), false)
	const newID = "33333333-3333-4333-8333-333333333333"
	s.mu.Lock()
	s.vmTags = cloneVMTags(state.ByVM)
	s.hosts[legacyfixture.HostID] = &models.Host{ID: legacyfixture.HostID}
	s.vms[legacyfixture.HostID] = []*models.VM{{UUID: legacyfixture.VMID, HostID: legacyfixture.HostID, Name: "synthetic-original"}, {UUID: newID, HostID: legacyfixture.HostID, Name: "synthetic-new"}}
	s.roles["fixture-role"].Permissions = append(s.roles["fixture-role"].Permissions, "vm.view", "vm.edit")
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("tag acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage", "vm.view", "vm.edit"})
	if err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	backend := func(root string) Backend {
		t.Helper()
		if mode == "mongo" {
			b := newMongoIntegrationBackend(t)
			requireInactiveGuestTTL(t, b)
			return b
		}
		return NewJSONBackend(filepath.Join(root, "state.json"))
	}
	b := backend(root)
	p := startAcceptance(t, binary, root, b, token, false)
	read := func(id string, want []string) {
		t.Helper()
		var got struct {
			Tags []string `json:"tags"`
		}
		if json.Unmarshal(p.request(t, "GET", "/api/vms/"+id+"/tags", nil, 200), &got) != nil || !reflect.DeepEqual(got.Tags, want) {
			t.Fatal("tag read differs", id, got.Tags)
		}
	}
	put := func(id string, tags, expected []string, status int) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"tags": tags, "expected_tags": expected})
		p.request(t, "PUT", "/api/vms/"+id+"/tags", raw, status)
	}
	originalCheck := func() {
		t.Helper()
		for _, pair := range [][2]string{{"vm_tags.bson", "vm_tags"}, {"vm_tags.indexes.bson", "index"}} {
			got, err := os.ReadFile(filepath.Join(source, pair[0]))
			want := fixture.Documents["vm_tags"]
			if pair[1] == "index" {
				want = fixture.Indexes["vm_tags"]
			}
			if err != nil || !bytes.Equal(got, want) {
				t.Fatal("immutable original changed", err)
			}
		}
		got, err := os.ReadFile(filepath.Join(stage, "original-vm_tags.bson"))
		if err != nil || !bytes.Equal(got, fixture.Documents["vm_tags"]) {
			t.Fatal("staged original changed", err)
		}
	}
	read(legacyfixture.VMID, []string{"database", "production"})
	p.request(t, "GET", "/api/vms/"+legacyfixture.OrphanTagVMID+"/tags", nil, 404)
	if !bytes.Contains(p.request(t, "GET", "/api/tags", nil, 200), []byte("retained-orphan")) {
		t.Fatal("orphan pruned")
	}
	if !bytes.Contains(p.request(t, "GET", "/api/vms?search=production", nil, 200), []byte(legacyfixture.VMID)) {
		t.Fatal("converted tag filter failed")
	}
	put(legacyfixture.VMID, []string{"edited"}, []string{"database", "production"}, 200)
	put(legacyfixture.VMID, []string{"wrong"}, []string{"database", "production"}, 409)
	put(newID, []string{"new"}, []string{}, 200)
	read(legacyfixture.VMID, []string{"edited"})
	read(newID, []string{"new"})
	originalCheck()
	p.stop(t, false)
	p = startAcceptance(t, binary, root, b, token, false)
	read(legacyfixture.VMID, []string{"edited"})
	read(newID, []string{"new"})
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restored := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restored, 0)
	want := &vmTagState{Version: 1, ByVM: map[string][]string{legacyfixture.VMID: {"edited"}, newID: {"new"}, legacyfixture.OrphanTagVMID: {"retained-orphan"}}}
	if !reflect.DeepEqual(snapshot.VMTagState, want) {
		t.Fatal("backup lost assignments/orphan")
	}
	b = backend(restored)
	p = startAcceptance(t, binary, restored, b, token, false)
	read(legacyfixture.VMID, []string{"edited"})
	read(newID, []string{"new"})
	put(legacyfixture.VMID, []string{}, []string{"edited"}, 200)
	read(legacyfixture.VMID, []string{})
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	delete(want.ByVM, legacyfixture.VMID)
	if !reflect.DeepEqual(second.VMTagState, want) {
		t.Fatal("clear/orphan lost in second backup")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restored, b, token, false)
	read(legacyfixture.VMID, []string{})
	read(newID, []string{"new"})
	if !bytes.Contains(p.request(t, "GET", "/api/tags", nil, 200), []byte("retained-orphan")) {
		t.Fatal("orphan lost after restore/restart")
	}
	p.stop(t, false)
	originalCheck()
	rawRestore := filepath.Join(root, "raw-restored")
	if err := os.Mkdir(rawRestore, 0700); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"source.json", "snapshot.json"}, {"original-vm_tags.bson", "vm_tags.bson"}, {"indexes-vm_tags.bson", "vm_tags.indexes.bson"}} {
		raw, err := os.ReadFile(filepath.Join(stage, pair[0]))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rawRestore, pair[1]), raw, 0400); err != nil {
			t.Fatal(err)
		}
	}
	recovered := filepath.Join(root, "recovered")
	run("-mode", "convert", "-source", rawRestore, "-bundle", recovered)
	if !bytes.Equal(exported, run("-mode", "legacy-tag-state", "-bundle", recovered)) {
		t.Fatal("raw restore/reconversion changed assignments")
	}
	emitAcceptance(t, "legacy_tag_lifecycle", map[string]any{"backend": mode, "fixture_schema": fixture.Schema, "source_contract": fixture.Source, "original_sha256": fixture.Checksums["vm_tags.bson"], "index_sha256": fixture.Checksums["vm_tags.indexes.bson"], "canonical_assignment_conversion": true, "api_read_edit_new_clear_conflict": true, "restart": true, "application_backup_fresh_restore": true, "second_backup_restart": true, "orphan_retained": true, "originals_unchanged": true, "raw_restore_reconversion": true, "originals_retained_separately": true, "app_backup_contains_original_bson": false, "tag_rule_selectors_verified": false, "legacy_pruning_resumed": false, "full_legacy_compatible": false})
}
