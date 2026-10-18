//go:build integration

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
)

func TestMongoIntegrationUnsupportedCachedNamespace(t *testing.T) {
	for _, name := range []string{"alarms", "affinity_rules", "future_feature"} {
		b := newMongoIntegrationBackend(t)
		d := &persistData{Hosts: map[string]*models.Host{"fixture": {ID: "fixture", CPUUsage: 1}}}
		if err := b.Save(t.Context(), d); err != nil {
			t.Fatal(err)
		}
		var before bson.Raw
		if err := b.db.Collection(mcoHosts).FindOne(t.Context(), bson.D{}).Decode(&before); err != nil {
			t.Fatal(err)
		}
		unsupported, _ := bson.Marshal(bson.D{{Key: "_id", Value: "synthetic"}, {Key: "enabled", Value: true}})
		if _, err := b.db.Collection(name).InsertOne(t.Context(), bson.Raw(unsupported)); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Load(t.Context()); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatal("cached load ignored namespace", err)
		}
		d.Hosts["fixture"].CPUUsage = 99
		if err := b.Save(t.Context(), d); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatal("cached save ignored namespace", err)
		}
		if _, err := b.captureEntityOverlay(t.Context(), d); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatal("cached backup omitted namespace", err)
		}
		for collection, want := range map[string]bson.Raw{mcoHosts: before, name: unsupported} {
			var got bson.Raw
			if err := b.db.Collection(collection).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || !bytes.Equal(want, got) {
				t.Fatal("refusal changed data", collection, err)
			}
		}
	}
}

func TestMongoIntegrationEmptyUnknownAndReadFailure(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	if err := b.db.CreateCollection(t.Context(), "future_empty"); err != nil {
		t.Fatal(err)
	}
	d, err := b.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Save(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if _, err := b.captureEntityOverlay(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	names, err := b.db.ListCollectionNames(t.Context(), bson.D{{Key: "name", Value: "future_empty"}})
	if err != nil || len(names) != 1 {
		t.Fatal("empty unknown collection removed", err)
	}
	if n, err := b.db.Collection("future_empty").CountDocuments(t.Context(), bson.D{}); err != nil || n != 0 {
		t.Fatal("empty unknown collection changed", err)
	}
	// A failed compatibility read is never interpreted as an empty/known schema.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := b.Load(ctx); err == nil {
		t.Fatal("load accepted failed preflight")
	}
	if err := b.Save(ctx, d); err == nil {
		t.Fatal("save accepted failed preflight")
	}
	if _, err := b.captureEntityOverlay(ctx, d); err == nil {
		t.Fatal("backup accepted failed preflight")
	}
}
