package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"kvm-center/internal/models"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

const tagFixtureUUID = "11111111-1111-4111-8111-111111111111"

func tagFixture() *vmTagState {
	return &vmTagState{Version: 1, ByVM: map[string][]string{tagFixtureUUID: {"database", "production"}}}
}
func TestTagFoundationOwnershipAndValidation(t *testing.T) {
	s := &Store{}
	tags := []string{" Production ", "DATABASE", "production"}
	if err := s.SetVMTags(" "+strings.ToUpper(tagFixtureUUID)+" ", tags); err != nil {
		t.Fatal(err)
	}
	tags[0] = "changed"
	got := s.GetVMTags(tagFixtureUUID)
	got[0] = "changed"
	if !reflect.DeepEqual(s.GetVMTags(tagFixtureUUID), []string{"database", "production"}) {
		t.Fatal("caller modified Store tags")
	}
	for _, input := range [][]string{{"bad tag"}, {strings.Repeat("x", 33)}, make([]string, 25)} {
		if err := s.SetVMTags(tagFixtureUUID, input); err == nil {
			t.Fatal("invalid edit accepted")
		}
		if len(s.GetVMTags(tagFixtureUUID)) != 2 {
			t.Fatal("invalid edit changed state")
		}
	}
	if err := s.SetVMTags("not-a-uuid", []string{"x"}); err == nil {
		t.Fatal("invalid identity accepted")
	}
	if err := s.SetVMTags(tagFixtureUUID, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.GetVMTags(tagFixtureUUID)) != 0 || len(s.vmTags) != 0 {
		t.Fatal("explicit clear did not remove assignment")
	}
}
func TestTagFoundationStrictJSON(t *testing.T) {
	for _, payload := range []string{
		`null`, `{}`, `{"version":99,"Version":1,"by_vm":{}}`, `{"version":1,"by_vm":{},"By_VM":{}}`, `{"version":1}`, `{"version":1,"by_vm":null}`, `{"version":2,"by_vm":{}}`,
		`{"version":1,"by_vm":{},"future":true}`, `{"version":1,"version":1,"by_vm":{}}`,
		fmt.Sprintf(`{"version":1,"by_vm":{"%s":["x"],"%s":["y"]}}`, tagFixtureUUID, tagFixtureUUID),
		fmt.Sprintf(`{"version":1,"by_vm":{"%s":["UPPER"]}}`, tagFixtureUUID),
		fmt.Sprintf(`{"version":1,"by_vm":{"%s":[]}}`, tagFixtureUUID),
		`{"version":1,"by_vm":{"invalid":["x"]}}`,
	} {
		var d persistData
		if err := json.Unmarshal([]byte(`{"vm_tag_state":`+payload+`}`), &d); err == nil {
			t.Fatalf("invalid payload accepted: %s", payload)
		}
	}
	for _, raw := range []string{`{"VM_TAGS":{}}`, `{"VM_TAG_STATE":null}`, `{"vm_tag_state":{"version":1,"by_vm":{}},"VM_TAG_STATE":null}`, `{"vm_tags":{}}`, `{"vm_tag_state":{"version":1,"by_vm":{}},"vm_tag_state":{"version":1,"by_vm":{}}}`} {
		var d persistData
		if err := json.Unmarshal([]byte(raw), &d); err == nil {
			t.Fatal("ambiguous/legacy tags silently accepted")
		}
	}
	for _, raw := range []string{`{}`, `{"vm_tag_state":{"version":1,"by_vm":{}}}`} {
		var d persistData
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatal(err)
		}
	}
	// Existing unrelated snapshot fields retain their prior permissive policy.
	var d persistData
	if err := json.Unmarshal([]byte(`{"future_unrelated":true}`), &d); err != nil {
		t.Fatal(err)
	}
}
func TestTagFoundationStrictBSON(t *testing.T) {
	for _, doc := range []bson.D{
		{{Key: "version", Value: 2}, {Key: "by_vm", Value: bson.M{}}},
		{{Key: "version", Value: 1}, {Key: "by_vm", Value: bson.M{}}, {Key: "future", Value: true}},
		{{Key: "version", Value: 1}, {Key: "version", Value: 1}, {Key: "by_vm", Value: bson.M{}}},
		{{Key: "version", Value: 1}, {Key: "by_vm", Value: bson.D{{Key: tagFixtureUUID, Value: []string{"x"}}, {Key: tagFixtureUUID, Value: []string{"y"}}}}},
		{{Key: "version", Value: 1}, {Key: "by_vm", Value: nil}},
	} {
		raw, err := bson.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var d vmTagState
		if err := bson.Unmarshal(raw, &d); err == nil {
			t.Fatal("unsupported BSON silently decoded")
		}
	}
	raw, err := bson.Marshal(tagFixture())
	if err != nil {
		t.Fatal(err)
	}
	var d vmTagState
	if err := bson.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&d, tagFixture()) {
		t.Fatal("BSON state changed")
	}
}
func TestTagFoundationSnapshotDirtyRetryAndClear(t *testing.T) {
	s, b := persistenceTestStore(t)
	if err := s.SetVMTags(tagFixtureUUID, []string{"production"}); err != nil {
		t.Fatal(err)
	}
	raw, err := s.ExportSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot persistData
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.VMTagState.ByVM[tagFixtureUUID], []string{"production"}) {
		t.Fatal("backup lost tags")
	}
	b.failNext = true
	if err := s.SaveChecked(); err == nil {
		t.Fatal("backend failure hidden")
	}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if len(b.attempts) != 2 || !isDirty(b.attempts[0], mcoVMTagState) || !isDirty(b.attempts[1], mcoVMTagState) {
		t.Fatal("backup/failure consumed dirty tags")
	}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if isDirty(b.attempts[2], mcoVMTagState) {
		t.Fatal("unchanged tags remained dirty")
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	dst := NewJSONBackend(path)
	if err := dst.Save(context.Background(), &snapshot); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStoreWithBackend(dst, path)
	reloaded.saveWG.Wait()
	t.Cleanup(func() { reloaded.saveWG.Wait() })
	if !reflect.DeepEqual(reloaded.GetVMTags(tagFixtureUUID), []string{"production"}) {
		t.Fatal("reload lost tags")
	}
	if err := reloaded.SetVMTags(tagFixtureUUID, nil); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	final, err := dst.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if final.VMTagState != nil {
		t.Fatal("cleared tags resurrected")
	}
	if err := s.SetVMTags(tagFixtureUUID, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if !isDirty(b.attempts[3], mcoVMTagState) || b.attempts[3].VMTagState != nil {
		t.Fatal("clear not represented as dirty absence")
	}
}
func TestTagFoundationUnknownLoadBlocksSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := []byte(`{"vm_tag_state":{"version":99,"by_vm":{}}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s := NewStoreWithBackend(NewJSONBackend(path), path)
	s.saveWG.Wait()
	if err := s.SaveChecked(); err == nil {
		t.Fatal("save gate opened after unsupported load")
	}
	got, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(got, raw) {
		t.Fatal("unsupported snapshot was changed")
	}
}
func TestTagFoundationLimitsAndPrettyBackup(t *testing.T) {
	state := &vmTagState{Version: 1, ByVM: map[string][]string{}}
	tags := make([]string, 24)
	for i := range tags {
		tags[i] = fmt.Sprintf("tag%02d%s", i, strings.Repeat("x", 27))
	}
	for i := 0; i < 800; i++ {
		state.ByVM[fmt.Sprintf("%08x-1111-4111-8111-111111111111", i)] = append([]string(nil), tags...)
	}
	raw, err := json.MarshalIndent(&persistData{VMTagState: state}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var got persistData
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("own pretty backup rejected: %v", err)
	}
	for i := 800; i < 2000; i++ {
		state.ByVM[fmt.Sprintf("%08x-1111-4111-8111-111111111111", i)] = append([]string(nil), tags...)
	}
	if err := state.validate(); err == nil {
		t.Fatal("aggregate encoded size not bounded")
	}
	state.ByVM = map[string][]string{}
	for i := 0; i <= maxTaggedVMs; i++ {
		state.ByVM[fmt.Sprintf("%08x-1111-4111-8111-111111111111", i)] = []string{"x"}
	}
	if err := state.validate(); err == nil {
		t.Fatal("assignment count not bounded")
	}
}
func TestTagFoundationDowngradeIsUnsupported(t *testing.T) {
	raw, err := json.Marshal(&persistData{VMTagState: tagFixture()})
	if err != nil {
		t.Fatal(err)
	}
	// Representative older JSON decoder: fields it cannot name are discarded.
	var old struct {
		VMNotes map[string]string `json:"vm_notes"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	downgraded, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var restored persistData
	if err := json.Unmarshal(downgraded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.VMTagState != nil {
		t.Fatal("test no longer demonstrates old codec field loss")
	}
}

func TestTagFoundationScheduledArchive(t *testing.T) {
	s, _ := persistenceTestStore(t)
	s.history = historyFixture()
	if err := s.SetVMTags(tagFixtureUUID, []string{"scheduled"}); err != nil {
		t.Fatal(err)
	}
	old := serverBackupPaths
	serverBackupPaths = backupPaths{}
	defer func() { serverBackupPaths = old }()
	dir := t.TempDir()
	if _, err := s.runScheduledBackup(&models.BackupSchedule{DestPath: dir}, time.Now()); err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.tar.gz"))
	if err != nil || len(names) != 1 {
		t.Fatal("scheduled archive missing")
	}
	file, err := os.Open(names[0])
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var manifest backupManifest
	var snapshot persistData
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		switch h.Name {
		case "manifest.json":
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
		case "store.json":
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatal(err)
			}
		}
	}
	if manifest.FeatureSchemas["host_history_state"] != 1 || snapshot.HostHistory == nil {
		t.Fatal("scheduled history schema/state missing")
	}
	if manifest.FeatureSchemas["vm_tag_state"] != 1 || snapshot.VMTagState == nil || !reflect.DeepEqual(snapshot.VMTagState.ByVM[tagFixtureUUID], []string{"scheduled"}) {
		t.Fatal("scheduled archive schema/state mismatch")
	}
}

type tagPartialBackend struct {
	stubBackend
	tags map[string][]string
	fail bool
}

func (b *tagPartialBackend) Save(_ context.Context, d *persistData) error {
	if isDirty(d, mcoVMTagState) {
		b.tags = nil
		if d.VMTagState != nil {
			b.tags = cloneVMTags(d.VMTagState.ByVM)
		}
	}
	if b.fail {
		return errors.New("later collection failed")
	}
	return nil
}
func TestTagFoundationPartialSaveReversion(t *testing.T) {
	b := &tagPartialBackend{}
	s := drainStore(b)
	for _, step := range []struct {
		tag  string
		fail bool
	}{{"original", false}, {"temporary", true}, {"original", false}} {
		b.fail = step.fail
		if err := s.SetVMTags(tagFixtureUUID, []string{step.tag}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveChecked(); (err != nil) != step.fail {
			t.Fatalf("unexpected write result: %v", err)
		}
	}
	if !reflect.DeepEqual(b.tags[tagFixtureUUID], []string{"original"}) {
		t.Fatal("partial tag write survived successful reversion")
	}
}
func TestTagFoundationMigrationVerification(t *testing.T) {
	src := &persistData{VMTagState: tagFixture()}
	back := &persistData{VMTagState: tagFixture()}
	back.VMTagState.ByVM[tagFixtureUUID] = []string{"different"}
	if err := verifyBackendRoundTrip(context.Background(), src, &stubBackend{stored: back}); err == nil {
		t.Fatal("same count hid lost tag values")
	}
	back.VMTagState = tagFixture()
	if err := verifyBackendRoundTrip(context.Background(), src, &stubBackend{stored: back}); err != nil {
		t.Fatal(err)
	}
}

func TestTagFoundationPerStoreDirtyIsolation(t *testing.T) {
	a, ba := persistenceTestStore(t)
	b, bb := persistenceTestStore(t)
	for _, s := range []*Store{a, b} {
		if err := s.SetVMTags(tagFixtureUUID, []string{"same"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if !isDirty(ba.attempts[0], mcoVMTagState) || !isDirty(bb.attempts[0], mcoVMTagState) {
		t.Fatal("one store consumed another store's tag dirty state")
	}
	if err := a.SetVMTags(tagFixtureUUID, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.GetVMTags(tagFixtureUUID), []string{"same"}) {
		t.Fatal("tag maps shared across stores")
	}
}
