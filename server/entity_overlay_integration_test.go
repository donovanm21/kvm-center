//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
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
