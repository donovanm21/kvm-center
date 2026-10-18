//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMongoIntegrationEntityOverlayFreshRestore(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	d := overlayFixture(t)
	if err := b.Save(t.Context(), d); err == nil {
		t.Fatal("ordinary save accepted recovery overlay")
	}
	if err := b.restoreEntitySnapshot(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	var got bson.Raw
	if err := b.db.Collection(mcoGroups).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Lookup("name").StringValue() != "edited" || got.Lookup("opaque").Type == 0 {
		t.Fatal("restore lost known or opaque values")
	}
	if err := b.restoreEntitySnapshot(t.Context(), d); err == nil {
		t.Fatal("nonempty/partial destination accepted")
	}
	var again bson.Raw
	if err := b.db.Collection(mcoGroups).FindOne(t.Context(), bson.D{}).Decode(&again); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, again) {
		t.Fatal("refused restore changed destination")
	}
}
func TestMongoIntegrationEntityOverlayBootstrapRefusesBeforeIndexes(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx := t.Context()
	// Drop only this test's owned database to remove initialization indexes.
	if err := b.db.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _ := bson.Marshal(bson.D{{Key: "_id", Value: "synthetic-expired"}, {Key: "expires_at", Value: time.Unix(1, 0)}})
	if _, err := b.db.Collection(mcoSessions).InsertOne(ctx, bson.Raw(raw)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	data, err := json.Marshal(overlayFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0400); err != nil {
		t.Fatal(err)
	}
	_, err = bootstrapBackendWith(ctx, b.uri, path, func() Backend { return NewMongoBackend(b.uri, b.dbName) }, func(context.Context) error { return nil })
	if err == nil {
		t.Fatal("nonempty recovery destination accepted")
	}
	var got bson.Raw
	if err := b.db.Collection(mcoSessions).FindOne(ctx, bson.D{}).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("refusal altered existing data")
	}
	names, err := b.db.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != mcoSessions {
		t.Fatal("refusal created collections")
	}
	cur, err := b.db.Collection(mcoSessions).Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var indexes []bson.M
	if err := cur.All(ctx, &indexes); err != nil {
		t.Fatal(err)
	}
	if len(indexes) != 1 {
		t.Fatal("refusal initialized indexes")
	}
}

func TestMongoIntegrationPassiveEntityRefusalAndNotesDeletion(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx := t.Context()
	for name, doc := range map[string]bson.D{
		"note type":      {{Key: "_id", Value: "vm"}, {Key: "vm_uuid", Value: "vm"}, {Key: "notes", Value: int32(1)}},
		"note identity":  {{Key: "_id", Value: "vm"}, {Key: "vm_uuid", Value: "different"}, {Key: "notes", Value: "text"}},
		"note duplicate": {{Key: "_id", Value: "vm"}, {Key: "vm_uuid", Value: "vm"}, {Key: "notes", Value: "a"}, {Key: "notes", Value: "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := bson.Marshal(doc)
			if _, err := b.db.Collection(mcoVMNotes).InsertOne(ctx, bson.Raw(raw)); err != nil {
				t.Fatal(err)
			}
			if _, err := b.Load(ctx); err == nil {
				t.Fatal("malformed note silently loaded")
			}
			if _, err := b.db.Collection(mcoVMNotes).DeleteMany(ctx, bson.D{}); err != nil {
				t.Fatal(err)
			}
		})
	}
	raw, _ := bson.Marshal(bson.D{{Key: "_id", Value: "vm"}, {Key: "vm_uuid", Value: "vm"}, {Key: "notes", Value: "old"}, {Key: "opaque", Value: []byte{7, 8}}})
	if _, err := b.db.Collection(mcoVMNotes).InsertOne(ctx, bson.Raw(raw)); err != nil {
		t.Fatal(err)
	}
	d, err := b.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d.VMNotes["vm"] = "new"
	if err := b.Save(ctx, d); err != nil {
		t.Fatal(err)
	}
	var got bson.Raw
	if err := b.db.Collection(mcoVMNotes).FindOne(ctx, bson.D{}).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Lookup("notes").StringValue() != "new" || !equalRaw(got.Lookup("opaque"), bson.Raw(raw).Lookup("opaque")) {
		t.Fatal("note write lost raw values")
	}
	delete(d.VMNotes, "vm")
	if err := b.Save(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.VMNotes["vm"] = "recreated"
	if err := b.Save(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := b.db.Collection(mcoVMNotes).FindOne(ctx, bson.D{}).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Lookup("opaque").Type != 0 {
		t.Fatal("deleted note resurrected opaque data")
	}
}

func TestMongoIntegrationPassivePreflightBeforeEntityWrites(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx := t.Context()
	seed, _ := bson.Marshal(bson.D{{Key: "_id", Value: "host"}, {Key: "id", Value: "host"}, {Key: "os", Value: "original"}})
	if _, err := b.db.Collection(mcoHosts).InsertOne(ctx, bson.Raw(seed)); err != nil {
		t.Fatal(err)
	}
	malformed, _ := bson.Marshal(bson.D{{Key: "_id", Value: "host|Pool"}, {Key: "hostid", Value: "host"}, {Key: "host_id", Value: "other"}, {Key: "name", Value: "Pool"}})
	if _, err := b.db.Collection(mcoStorage).InsertOne(ctx, bson.Raw(malformed)); err != nil {
		t.Fatal(err)
	}
	d := &persistData{Hosts: map[string]*models.Host{"host": {ID: "host", OS: "edited"}}, Storage: map[string][]*models.StoragePool{"host": {{HostID: "host", Name: "Pool"}}}}
	if err := b.Save(ctx, d); err == nil {
		t.Fatal("ambiguous storage alias accepted")
	}
	var got bson.Raw
	if err := b.db.Collection(mcoHosts).FindOne(ctx, bson.D{}).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, seed) {
		t.Fatal("host changed before passive preflight refusal")
	}
	if _, err := b.Load(ctx); err == nil {
		t.Fatal("ambiguous storage alias loaded")
	}
}
