//go:build integration

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
	"kvm-center/internal/models"
)

// Original fixtures exist only as immutable raw BSON in memory. Save/Load and
// index creation run only against a fresh disposable synthetic destination.
func TestMongoIntegrationCanonicalBSONStaging(t *testing.T) {
	for _, form := range []string{"canonical", "old", "equal-dual"} {
		t.Run(form, func(t *testing.T) {
			b := newMongoIntegrationBackend(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			expiry := time.Date(2099, 1, 2, 3, 4, 5, 678000000, time.UTC)
			var vm models.VM
			var session models.Session
			var migration models.MigrationRecord
			var audit models.AuditEvent
			specs := []struct {
				collection, key, old, id string
				value                    any
				kind                     bsontype.Type
				doc                      bson.D
				target                   any
				direction                int32
			}{
				{mcoVMs, "host_id", "hostid", "fixture-vm", "fixture-host", bsontype.String, bson.D{{Key: "uuid", Value: "fixture-vm"}}, &vm, 1},
				{mcoSessions, "expires_at", "expiresat", "fixture-session", expiry, bsontype.DateTime, bson.D{{Key: "token", Value: "fixture-session"}, {Key: "userid", Value: "synthetic-owner"}}, &session, 1},
				{mcoMigrations, "started_at", "startedat", "fixture-migration", int64(1792108800), bsontype.Int64, bson.D{{Key: "id", Value: "fixture-migration"}, {Key: "status", Value: "running"}}, &migration, -1},
				{mcoAuditEvents, "actor_id", "actorid", "fixture-audit", "fixture-actor", bsontype.String, bson.D{{Key: "id", Value: "fixture-audit"}, {Key: "at", Value: expiry}}, &audit, 1},
			}
			originals := map[string][]byte{}
			snapshots := map[string][]byte{}
			for _, s := range specs {
				doc := append(bson.D(nil), s.doc...)
				if form != "old" {
					doc = append(doc, bson.E{Key: s.key, Value: s.value})
				}
				if form != "canonical" {
					doc = append(doc, bson.E{Key: s.old, Value: s.value})
				}
				// Unsupported source data is retained by the original snapshot, not the model.
				doc = append(doc, bson.E{Key: "legacy_opaque", Value: bson.D{{Key: "original", Value: int32(7)}}})
				raw, err := bson.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				originals[s.collection] = raw
				snapshots[s.collection] = append([]byte(nil), raw...)
				if err = bson.Unmarshal(raw, s.target); err != nil {
					t.Fatal(err)
				}
			}
			data := &persistData{VMs: map[string][]*models.VM{vm.HostID: {&vm}}, Sessions: map[string]*models.Session{session.Token: &session}, Migrations: []*models.MigrationRecord{&migration}, Audit: []*models.AuditEvent{&audit}}
			if err := b.Save(ctx, data); err != nil {
				t.Fatal(err)
			}
			if err := b.Close(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := b.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.VMs["fixture-host"]) != 1 || got.VMs["fixture-host"][0].HostID != "fixture-host" || got.Sessions["fixture-session"] == nil || !got.Sessions["fixture-session"].ExpiresAt.Equal(expiry) || len(got.Migrations) != 1 || got.Migrations[0].StartedAt != 1792108800 || len(got.Audit) != 1 || got.Audit[0].ActorID != "fixture-actor" || !got.Audit[0].At.Equal(expiry) {
				t.Fatal("canonical fields lost after fresh load")
			}
			db, err := b.dial(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range specs {
				var raw bson.Raw
				if err := db.Collection(s.collection).FindOne(ctx, bson.D{{Key: "_id", Value: s.id}}).Decode(&raw); err != nil {
					t.Fatal(err)
				}
				if raw.Lookup(s.key).Type != s.kind || raw.Lookup(s.old).Type != 0 {
					t.Fatal("canonical persisted key/type missing or old alias retained")
				}
				// Exact scalar value, including milliseconds, must match the source BSON.
				expected, _ := bson.Marshal(bson.D{{Key: s.key, Value: s.value}})
				if !bytes.Equal(raw.Lookup(s.key).Value, bson.Raw(expected).Lookup(s.key).Value) {
					t.Fatal("persisted scalar changed")
				}
				cur, err := db.Collection(s.collection).Indexes().List(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var indexes []bson.Raw
				if err = cur.All(ctx, &indexes); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, index := range indexes {
					keys := index.Lookup("key").Document()
					if v := keys.Lookup(s.key); v.Type == bsontype.Int32 && v.Int32() == s.direction {
						found = true
						ttl, ok := index.Lookup("expireAfterSeconds").Int32OK()
						if s.collection == mcoSessions && (!ok || ttl != 0) {
							t.Fatal("session TTL contract changed")
						}
					}
				}
				if !found {
					t.Fatalf("canonical index missing for %s", s.collection)
				}
				if !bytes.Equal(originals[s.collection], snapshots[s.collection]) {
					t.Fatal("original raw BSON changed")
				}
				if bson.Raw(originals[s.collection]).Lookup("legacy_opaque").Type != bsontype.EmbeddedDocument {
					t.Fatal("original unsupported data lost")
				}
				if s.collection == mcoMigrations && raw.Lookup("expires_at").Type != 0 {
					t.Fatal("active synthetic migration acquired retention expiry")
				}
			}
		})
	}
}
