package main

import (
	"bytes"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
	"path/filepath"
	"reflect"
	"testing"
)

func overlayFixture(t *testing.T) *persistData {
	t.Helper()
	raw, err := bson.Marshal(bson.D{{Key: "_id", Value: "group"}, {Key: "id", Value: "group"}, {Key: "name", Value: "original"}, {Key: "opaque", Value: primitive.NewDecimal128(7, 9)}})
	if err != nil {
		t.Fatal(err)
	}
	return &persistData{Groups: map[string]*models.Group{"group": {ID: "group", Name: "edited"}}, EntityOverlay: &entityOverlay{Version: 1, Collections: map[string]map[string][]byte{mcoGroups: {"group": raw}}}}
}
func TestEntityOverlayJSONRoundTripAndDeletion(t *testing.T) {
	d := overlayFixture(t)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back persistData
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := bson.Raw(back.EntityOverlay.Collections[mcoGroups]["group"])
	if got.Lookup("name").StringValue() != "edited" || !equalRaw(got.Lookup("opaque"), bson.Raw(d.EntityOverlay.Collections[mcoGroups]["group"]).Lookup("opaque")) {
		t.Fatal("typed edit or opaque value lost")
	}
	features, err := snapshotFeatureSchemas(raw)
	if err != nil || features["entity_bson_overlay"] != 7 {
		t.Fatal("missing overlay requirement")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	b := NewJSONBackend(path)
	if err := b.Save(t.Context(), &back); err != nil {
		t.Fatal(err)
	}
	s := NewStoreWithBackend(b, path)
	s.saveWG.Wait()
	s.mu.Lock()
	delete(s.groups, "group")
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.groups["group"] = &models.Group{ID: "group", Name: "recreated"}
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	restored, err := b.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if bson.Raw(restored.EntityOverlay.Collections[mcoGroups]["group"]).Lookup("opaque").Type != 0 {
		t.Fatal("deleted opaque value resurrected")
	}
}
func TestEntityOverlayRefusals(t *testing.T) {
	valid, err := json.Marshal(overlayFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"null":               []byte(`{"entity_bson_overlay":null}`),
		"case":               []byte(`{"ENTITY_BSON_OVERLAY":null}`),
		"version":            bytes.Replace(valid, []byte(`"version":7`), []byte(`"version":99`), 1),
		"duplicate":          bytes.Replace(valid, []byte(`"version":7`), []byte(`"version":6,"version":6`), 1),
		"unknown collection": bytes.Replace(valid, []byte(`"collections":{"api_tokens"`), []byte(`"collections":{"unsupported"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			var d persistData
			if err := json.Unmarshal(raw, &d); err == nil {
				t.Fatal("unsupported overlay accepted")
			}
		})
	}
	d := overlayFixture(t)
	d.EntityOverlay.Collections[mcoGroups]["other"] = d.EntityOverlay.Collections[mcoGroups]["group"]
	if _, err := json.Marshal(d); err == nil {
		t.Fatal("mismatched identity accepted")
	}
	d = overlayFixture(t)
	d.Groups["group"] = nil
	if _, err := json.Marshal(d); err == nil {
		t.Fatal("nil entity accepted")
	}
}
func TestBSONPreservationDeployedStartupEntities(t *testing.T) {
	f := legacyfixture.GenerateDeployed()
	for _, col := range []string{mcoUsers, mcoGroups, mcoRoles, mcoStorage, mcoRemovedHosts} {
		t.Run(col, func(t *testing.T) {
			typ, aliases, err := entitySchema(col)
			if err != nil {
				t.Fatal(err)
			}
			raw := bson.Raw(f.Documents[col])
			if _, err := entityIdentity(raw, col); err != nil {
				t.Fatal(err)
			}
			// Decode and re-encode through storage JSON, preserving every declared field.
			value := reflect.New(typ).Interface()
			if err := bson.Unmarshal(raw, value); err != nil {
				t.Fatal(err)
			}
			changed := "description"
			field := "Description"
			if col == mcoRemovedHosts {
				changed = "reason"
				field = "Reason"
			}
			if col == mcoStorage {
				changed = "path"
				field = "Path"
			}
			if col == mcoUsers {
				changed = "email"
				field = "Email"
			}
			reflect.ValueOf(value).Elem().FieldByName(field).SetString("synthetic edit")
			candidate, _ := bson.Marshal(value)
			es, _ := bson.Raw(candidate).Elements()
			doc := bson.D{{Key: "_id", Value: raw.Lookup("_id")}}
			for _, e := range es {
				doc = append(doc, bson.E{Key: e.Key(), Value: e.Value()})
			}
			candidate, _ = bson.Marshal(doc)
			got, err := preserveEntityBSON(raw, candidate, typ, aliases)
			if err != nil {
				t.Fatal(err)
			}
			es, _ = raw.Elements()
			for _, e := range es {
				if e.Key() != changed && !equalRaw(e.Value(), got.Lookup(e.Key())) {
					t.Fatalf("lost field %s", e.Key())
				}
			}
			if got.Lookup(changed).StringValue() != "synthetic edit" {
				t.Fatal("known edit lost")
			}
		})
	}
}

func TestEntityOverlayRejectsIncompatibleContainer(t *testing.T) {
	raw, _ := bson.Marshal(bson.D{{Key: "_id", Value: "vm"}, {Key: "uuid", Value: "vm"}, {Key: "host_id", Value: "host"}, {Key: "networks", Value: bson.A{bson.D{{Key: "mac", Value: "old"}, {Key: "opaque", Value: int64(7)}}}}})
	var vm models.VM
	if err := bson.Unmarshal(raw, &vm); err != nil {
		t.Fatal(err)
	}
	vm.Networks[0].MAC = "changed"
	d := &persistData{VMs: map[string][]*models.VM{"host": {&vm}}, EntityOverlay: &entityOverlay{Version: 1, Collections: map[string]map[string][]byte{mcoVMs: {"vm": raw}}}}
	if _, err := json.Marshal(d); err == nil {
		t.Fatal("unrestorable opaque container edit exported")
	}
}

func TestEntityOverlayRejectsTypedJSONCaseAmbiguity(t *testing.T) {
	valid, err := json.Marshal(overlayFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"top":    append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"Groups":null}`)...),
		"nested": bytes.Replace(valid, []byte(`"name":"edited"`), []byte(`"name":"edited","Name":"lost"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			var d persistData
			if err := json.Unmarshal(raw, &d); err == nil {
				t.Fatal("case-ambiguous typed fields accepted")
			}
		})
	}
	// Arbitrary identity map keys remain case-sensitive.
	var d persistData
	if err := json.Unmarshal([]byte(`{"groups":{"A":{"id":"A"},"a":{"id":"a"}}}`), &d); err != nil || len(d.Groups) != 2 {
		t.Fatal("identity keys treated as struct fields")
	}
}

func TestPassiveOverlayVersionsAndIdentity(t *testing.T) {
	raw, _ := bson.Marshal(bson.D{{Key: "_id", Value: "host|Pool"}, {Key: "hostid", Value: "host"}, {Key: "name", Value: "Pool"}, {Key: "opaque", Value: primitive.NewDecimal128(23, 29)}})
	d := &persistData{Storage: map[string][]*models.StoragePool{"host": {{HostID: "host", Name: "Pool", AllocationGB: 25}}}, VMNotes: map[string]string{"vm": "edited"}, EntityOverlay: &entityOverlay{Version: 2, Collections: map[string]map[string][]byte{mcoStorage: {"host|Pool": raw}}}}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back persistData
	if err = json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	got := bson.Raw(back.EntityOverlay.Collections[mcoStorage]["host|Pool"])
	if got.Lookup("allocationgb").Double() != 25 || !equalRaw(got.Lookup("opaque"), bson.Raw(raw).Lookup("opaque")) {
		t.Fatal("passive overlay lost values")
	}
	d.EntityOverlay.Version = 1
	if _, err = json.Marshal(d); err == nil {
		t.Fatal("v1 accepted v2 collection")
	}
	d.EntityOverlay.Version = 2
	for name, mutate := range map[string]func(){
		"host mismatch": func() { d.Storage["host"][0].HostID = "other" },
		"delimiter":     func() { d.Storage["host"][0].Name = "a|b" },
		"duplicate":     func() { d.Storage["host"] = append(d.Storage["host"], d.Storage["host"][0]) },
		"nil":           func() { d.Storage["host"][0] = nil },
		"note case":     func() { d.VMNotes["VM"] = "ambiguous" },
		"note conflict": func() { d.VMs = map[string][]*models.VM{"host": {{UUID: "vm", Notes: "different"}}} },
	} {
		t.Run(name, func(t *testing.T) {
			var fresh persistData
			if err := json.Unmarshal(encoded, &fresh); err != nil {
				t.Fatal(err)
			}
			d = &fresh
			mutate()
			if _, err := json.Marshal(d); err == nil {
				t.Fatal("ambiguous passive entity accepted")
			}
		})
	}
}

func TestOverlayV1ManifestRequirementsRemainV1(t *testing.T) {
	d := overlayFixture(t)
	raw, err := json.Marshal(struct {
		Groups  map[string]*models.Group `json:"groups"`
		Overlay *entityOverlay           `json:"entity_bson_overlay"`
	}{d.Groups, d.EntityOverlay})
	if err != nil {
		t.Fatal(err)
	}
	features, err := snapshotFeatureSchemas(raw)
	if err != nil || features["entity_bson_overlay"] != 1 {
		t.Fatal("v1 manifest rejected", features, err)
	}
	var upgraded persistData
	if err := json.Unmarshal(raw, &upgraded); err != nil {
		t.Fatal(err)
	}
	if upgraded.EntityOverlay.Version != 7 {
		t.Fatal("validated v1 failed upgrade")
	}
}

func TestRetirementOverlayVersionsAndIdentity(t *testing.T) {
	original := bson.D{{Key: "_id", Value: "Retired.Example"}, {Key: "hostid", Value: "Retired.Example"}, {Key: "reason", Value: "old"}, {Key: "opaque", Value: primitive.NewDecimal128(41, 43)}}
	raw, _ := bson.Marshal(original)
	d := &persistData{RemovedHosts: map[string]*models.RemovedHost{"retired.example": {HostID: "Retired.Example", Reason: "edited"}}, EntityOverlay: &entityOverlay{Version: 3, Collections: map[string]map[string][]byte{mcoRemovedHosts: {"Retired.Example": raw}}}}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back persistData
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	got := bson.Raw(back.EntityOverlay.Collections[mcoRemovedHosts]["Retired.Example"])
	if got.Lookup("reason").StringValue() != "edited" || !equalRaw(got.Lookup("opaque"), bson.Raw(raw).Lookup("opaque")) {
		t.Fatal("retirement values lost")
	}
	for _, version := range []int{1, 2} {
		d.EntityOverlay.Version = version
		if _, err := json.Marshal(d); err == nil {
			t.Fatal("old overlay accepted retirement", version)
		}
	}
	d.EntityOverlay.Version = 3
	d.RemovedHosts = map[string]*models.RemovedHost{"Retired.Example": {HostID: "Retired.Example"}}
	if _, err := json.Marshal(d); err == nil {
		t.Fatal("noncanonical lookup key accepted")
	}
	d.RemovedHosts = back.RemovedHosts
	other, _ := bson.Marshal(bson.D{{Key: "_id", Value: "retired.example"}, {Key: "hostid", Value: "retired.example"}})
	d.EntityOverlay.Collections[mcoRemovedHosts]["retired.example"] = other
	if _, err := json.Marshal(d); err == nil {
		t.Fatal("case-colliding retirement overlay accepted")
	}
	for _, alias := range []string{"host_id", "HOST_ID", "removed_at", "Removed_By"} {
		doc := append(bson.D(nil), original...)
		doc = append(doc, bson.E{Key: alias, Value: "ambiguous"})
		bad, _ := bson.Marshal(doc)
		if _, err := entityIdentity(bad, mcoRemovedHosts); err == nil {
			t.Fatal("retirement alias accepted", alias)
		}
	}
}
