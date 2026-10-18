//go:build integration

package main

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"reflect"
	"testing"
)

func TestMongoIntegrationGuestUpdateRecovery(t *testing.T) {
	ctx := context.Background()
	b := newMongoIntegrationBackend(t)
	state := &guestUpdateState{Version: 1, Requests: []guestUpdateRequest{{ID: "request", Version: Version, Revision: GitSHA, Targets: []guestUpdateTarget{{UUID: updateVM1, Phase: "queued"}}}}}
	d := &persistData{GuestUpdates: state}
	if err := b.restoreEntitySnapshot(ctx, d); err != nil {
		t.Fatal(err)
	}
	stale := &persistData{GuestUpdates: state.clone()}
	d.GuestUpdates.Requests[0].Targets[0] = guestUpdateTarget{UUID: updateVM1, Phase: "offered_unconfirmed", URL: "https://example.invalid/binary", OS: "linux", Arch: "amd64"}
	if err := b.Save(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := b.captureEntityOverlay(ctx, stale); err == nil {
		t.Fatal("stale queued export erased offer barrier")
	}
	got, err := b.Load(ctx)
	if err != nil || !reflect.DeepEqual(got.GuestUpdates, d.GuestUpdates) {
		t.Fatal("offer lost", err)
	}
	fresh := newMongoIntegrationBackend(t)
	if err = fresh.restoreEntitySnapshot(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err = verifyBackendRoundTrip(ctx, got, fresh); err != nil {
		t.Fatal(err)
	}
	if err = b.Save(ctx, stale); err == nil {
		t.Fatal("offer reverted")
	}
	if err = b.Save(ctx, &persistData{}); err == nil {
		t.Fatal("ledger erased")
	}
}

func TestMongoIntegrationGuestUpdateRefusalBeforeWrites(t *testing.T) {
	ctx := context.Background()
	b := newMongoIntegrationBackend(t)
	db, err := b.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	col := db.Collection(guestUpdateCollection)
	if _, err = col.InsertOne(ctx, bson.D{{Key: "_id", Value: "singleton"}, {Key: "payload", Value: `{"version":99}`}}); err != nil {
		t.Fatal(err)
	}
	if err = rejectLegacyCollections(ctx, db); err == nil {
		t.Fatal("malformed ledger passed startup preflight")
	}
	if _, err = b.captureEntityOverlay(ctx, &persistData{}); err == nil {
		t.Fatal("malformed cached export allowed")
	}
	if _, err = b.Load(ctx); err == nil {
		t.Fatal("malformed loaded")
	}
	if err = b.Save(ctx, &persistData{}); err == nil {
		t.Fatal("malformed overwritten")
	}
	raw, _ := json.Marshal(&guestUpdateState{Version: 1, Requests: []guestUpdateRequest{}})
	if _, err = col.UpdateOne(ctx, bson.M{"_id": "singleton"}, bson.M{"$set": bson.M{"payload": string(raw)}}); err != nil {
		t.Fatal(err)
	}
	if _, err = col.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "expires", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)}); err != nil {
		t.Fatal(err)
	}
	if err = rejectLegacyCollections(ctx, db); err == nil {
		t.Fatal("TTL ledger passed startup preflight")
	}
	if _, err = b.captureEntityOverlay(ctx, &persistData{}); err == nil {
		t.Fatal("TTL cached export allowed")
	}
}
