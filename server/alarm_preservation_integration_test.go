//go:build integration

package main

import (
	"bytes"
	"go.mongodb.org/mongo-driver/bson"
	"testing"
)

func TestMongoIntegrationResourceAlarmPreservation(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	raw := alarmFixture(t, "original")
	if _, err := b.db.Collection(mcoAlarms).InsertOne(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	d, err := b.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	d.Alarms[0].Threshold = 85
	if err := b.Save(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	var got bson.Raw
	if err := b.db.Collection(mcoAlarms).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !equalRaw(got.Lookup("opaque"), raw.Lookup("opaque")) || got.Lookup("threshold").Double() != 85 {
		t.Fatal("known write lost alarm values")
	}
	d.EntityOverlay, err = b.captureEntityOverlay(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	fresh := newMongoIntegrationBackend(t)
	if err := fresh.restoreEntitySnapshot(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	var restored bson.Raw
	if err := fresh.db.Collection(mcoAlarms).FindOne(t.Context(), bson.D{}).Decode(&restored); err != nil || !bytes.Equal(got, restored) {
		t.Fatal("restore changed alarm", err)
	}
	d.EntityOverlay = nil
	d.Alarms = nil
	if err := b.Save(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	d.EntityOverlay, err = b.captureEntityOverlay(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	empty := newMongoIntegrationBackend(t)
	if err := empty.restoreEntitySnapshot(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	n, err := empty.db.Collection(mcoAlarms).CountDocuments(t.Context(), bson.D{})
	if err != nil || n != 0 {
		t.Fatal("deleted alarm resurrected")
	}
}
func TestMongoIntegrationAlarmEmptyArraysAndAdmission(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	var doc bson.D
	bson.Unmarshal(alarmFixture(t, "original"), &doc)
	for i := range doc {
		if doc[i].Key == "hosts" || doc[i].Key == "vms" {
			doc[i].Value = bson.A{}
		}
	}
	raw, _ := bson.Marshal(doc)
	if _, err := b.db.Collection(mcoAlarms).InsertOne(t.Context(), bson.Raw(raw)); err != nil {
		t.Fatal(err)
	}
	s := NewStoreWithBackend(b, t.TempDir()+"/state.json")
	s.saveWG.Wait()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	var got bson.Raw
	if err := b.db.Collection(mcoAlarms).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"hosts", "vms", "opaque"} {
		if !equalRaw(got.Lookup(key), bson.Raw(raw).Lookup(key)) {
			t.Fatal("unrelated save changed array", key)
		}
	}
	malformed := append(doc, bson.E{Key: "for_seconds", Value: int32(0)})
	bad, _ := bson.Marshal(malformed)
	if _, err := b.db.Collection(mcoAlarms).ReplaceOne(t.Context(), bson.D{}, bson.Raw(bad)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetResourceAlarms(nil); err == nil {
		t.Fatal("replacement erased unsupported original")
	}
	if err := s.SaveChecked(); err == nil {
		t.Fatal("cached save accepted ambiguity")
	}
	if _, err := s.ExportSnapshot(); err == nil {
		t.Fatal("backup accepted ambiguity")
	}
	if err := b.db.Collection(mcoAlarms).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || !bytes.Equal(got, bad) {
		t.Fatal("refusal changed original")
	}
}

func TestMongoIntegrationAlarmInventoryRefusal(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	original := remainingAlarmFixture(t, "vm-alarm", alarmVMStopped)
	if _, err := b.db.Collection(mcoAlarms).InsertOne(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		name := "fixture-vm"
		if id == "b" {
			name = "other-vm"
		}
		if _, err := b.db.Collection(mcoHosts).InsertOne(t.Context(), bson.D{{Key: "_id", Value: id}, {Key: "id", Value: id}}); err != nil {
			t.Fatal(err)
		}
		if _, err := b.db.Collection(mcoVMs).InsertOne(t.Context(), bson.D{{Key: "_id", Value: id}, {Key: "uuid", Value: id}, {Key: "host_id", Value: id}, {Key: "name", Value: name}, {Key: "state", Value: "stopped"}, {Key: "autostart", Value: true}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := preflightCollections(t.Context(), b.db); err != nil {
		t.Fatal("valid nonempty inventory refused", err)
	}
	if _, err := b.Load(t.Context()); err != nil {
		t.Fatal("valid nonempty inventory load", err)
	}
	if _, err := b.db.Collection(mcoVMs).UpdateOne(t.Context(), bson.D{{Key: "_id", Value: "b"}}, bson.D{{Key: "$set", Value: bson.D{{Key: "name", Value: "fixture-vm"}}}}); err != nil {
		t.Fatal(err)
	}
	if preflightCollections(t.Context(), b.db) == nil {
		t.Fatal("ambiguous VM target admitted before indexes")
	}
	if _, err := b.Load(t.Context()); err == nil {
		t.Fatal("ambiguous VM target loaded")
	}
	var got bson.Raw
	if err := b.db.Collection(mcoAlarms).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || !bytes.Equal(got, original) {
		t.Fatal("refusal changed alarm original")
	}
	// A stale cached snapshot cannot erase the unsupported raw inventory on Save/export.
	clean := &persistData{}
	if _, err := prepareEntityDocuments(t.Context(), b.db, mcoAlarms, nil); err == nil {
		t.Fatal("cached preservation erased ambiguous inventory")
	}
	if _, err := b.captureEntityOverlay(t.Context(), clean); err == nil {
		t.Fatal("cached export accepted ambiguous inventory")
	}
}
