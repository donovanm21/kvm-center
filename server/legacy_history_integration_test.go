//go:build integration

package main

import (
	"bytes"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"reflect"
	"testing"
)

func TestMongoIntegrationActiveLegacyHistory(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	state := legacyStateFixture(t)
	d := &persistData{LegacyHistory: state}
	if err := b.restoreEntitySnapshot(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	back, err := b.Load(t.Context())
	if err != nil || !reflect.DeepEqual(back.LegacyHistory, state) {
		t.Fatal("restore/read", err)
	}
	if err := b.Save(t.Context(), back); err != nil {
		t.Fatal("ordinary save", err)
	}
	id := ""
	for key := range state.Records {
		id = key
	}
	var raw bson.Raw
	if err := b.db.Collection(mcoLegacyHistory).FindOne(t.Context(), bson.D{{Key: "_id", Value: id}}).Decode(&raw); err != nil || !bytes.Equal(raw, state.Records[id]) {
		t.Fatal("original changed", err)
	}
	if err := b.restoreEntitySnapshot(t.Context(), d); err == nil {
		t.Fatal("nonempty restore accepted")
	}
	mutated := *back
	mutated.LegacyHistory = nil
	if err := b.Save(t.Context(), &mutated); err == nil {
		t.Fatal("removal accepted")
	}
	if _, err := b.db.Collection(mcoLegacyHistory).DeleteOne(t.Context(), bson.D{{Key: "_id", Value: id}}); err != nil {
		t.Fatal(err)
	}
	for _, call := range []func() error{func() error { _, err := b.Load(t.Context()); return err }, func() error { return b.Save(t.Context(), back) }, func() error { _, err := b.captureEntityOverlay(t.Context(), back); return err }} {
		if call() == nil {
			t.Fatal("cached deletion not refused")
		}
	}
	n, err := b.db.Collection(mcoLegacyHistory).CountDocuments(t.Context(), bson.D{})
	if err != nil || n != 0 {
		t.Fatal("missing legacy resurrected", err)
	}
}
func TestMongoIntegrationActiveLegacyHistoryTampering(t *testing.T) {
	for _, kind := range []string{"marker", "raw", "additional", "index"} {
		t.Run(kind, func(t *testing.T) {
			b := newMongoIntegrationBackend(t)
			state := legacyStateFixture(t)
			if err := b.restoreEntitySnapshot(t.Context(), &persistData{LegacyHistory: state}); err != nil {
				t.Fatal(err)
			}
			back, err := b.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "marker":
				_, err = b.db.Collection(mcoLegacyHistoryContract).UpdateOne(t.Context(), bson.D{}, bson.D{{Key: "$set", Value: bson.D{{Key: "sha256", Value: "wrong"}}}})
			case "raw":
				_, err = b.db.Collection(mcoLegacyHistory).UpdateOne(t.Context(), bson.D{}, bson.D{{Key: "$set", Value: bson.D{{Key: "opaque", Value: true}}}})
			case "additional":
				_, err = b.db.Collection(mcoLegacyHistory).InsertOne(t.Context(), bson.D{{Key: "_id", Value: "extra"}})
			case "index":
				_, err = b.db.Collection(mcoLegacyHistory).Indexes().CreateOne(t.Context(), mongo.IndexModel{Keys: bson.D{{Key: "host", Value: 1}}})
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range []func() error{func() error { _, err := b.Load(t.Context()); return err }, func() error { return b.Save(t.Context(), back) }, func() error { _, err := b.captureEntityOverlay(t.Context(), back); return err }} {
				if call() == nil {
					t.Fatal("cached tamper not refused")
				}
			}
		})
	}
}
