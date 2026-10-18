//go:build integration

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestMongoIntegrationLegacyRefusalBeforeIndexes(t *testing.T) {
	for _, legacy := range []string{"perf_history", "vm_tags", "alarms", "affinity_rules", "future_feature"} {
		t.Run(legacy, func(t *testing.T) {
			b := newMongoIntegrationBackend(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// Remove only this random owned empty database's initialization indexes
			// before seeding the legacy fixture. No legacy data ever has TTL active.
			if err := b.db.Drop(ctx); err != nil {
				t.Fatal(err)
			}
			if err := b.Close(ctx); err != nil {
				t.Fatal(err)
			}
			cli, err := mongo.Connect(ctx, options.Client().ApplyURI(b.uri))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Disconnect(ctx)
			db := cli.Database(b.dbName)
			for name, doc := range map[string]bson.D{
				legacy:     {{Key: "_id", Value: "legacy"}, {Key: "points", Value: bson.A{}}},
				"sessions": {{Key: "_id", Value: "expired"}, {Key: "expires_at", Value: time.Unix(1, 0)}},
			} {
				if _, err := db.Collection(name).InsertOne(ctx, doc); err != nil {
					t.Fatal(err)
				}
			}
			var before bson.Raw
			if err := db.Collection("sessions").FindOne(ctx, bson.D{}).Decode(&before); err != nil {
				t.Fatal(err)
			}
			// Repeated calls must not cache a connection that bypasses refusal.
			for i := 0; i < 2; i++ {
				if _, err := b.Load(ctx); err == nil || !strings.Contains(err.Error(), legacy) {
					t.Fatalf("wrong refusal: %v", err)
				}
				if b.client != nil || b.db != nil {
					t.Fatal("rejected connection cached")
				}
			}
			if err := b.Save(ctx, &persistData{}); err == nil || !strings.Contains(err.Error(), legacy) {
				t.Fatalf("write did not refuse: %v", err)
			}
			names, err := db.ListCollectionNames(ctx, bson.D{})
			if err != nil || len(names) != 2 {
				t.Fatalf("refusal created collections: %v %v", names, err)
			}
			for _, name := range names {
				cur, err := db.Collection(name).Indexes().List(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var indexes []bson.M
				if err := cur.All(ctx, &indexes); err != nil {
					t.Fatal(err)
				}
				if len(indexes) != 1 || indexes[0]["name"] != "_id_" {
					t.Fatalf("indexes changed for %s: %v", name, indexes)
				}
			}
			var after bson.Raw
			if err := db.Collection("sessions").FindOne(ctx, bson.D{}).Decode(&after); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("expired fixture changed")
			}
		})
	}
}
