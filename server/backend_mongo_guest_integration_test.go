//go:build integration

package main

import (
	"context"
	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
	"reflect"
	"testing"
	"time"
)

func TestMongoIntegrationGuestEnvelopeReconnect(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stamp := time.Date(2100, 1, 1, 0, 0, 0, 123000000, time.UTC)
	want := &guestEntry{UpdatedAt: stamp, Payload: models.GuestTelemetryPayload{VMUUID: "synthetic-vm", Hostname: "synthetic-guest", MemTotalMB: 4096, IPs: []string{"192.0.2.40"}, Timestamp: stamp}}
	data := &persistData{GuestData: map[string]*guestEntry{"synthetic-vm": want}}
	for i := 0; i < 2; i++ {
		if err := b.Save(ctx, data); err != nil {
			t.Fatal(err)
		}
		db, err := b.dial(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var raw bson.Raw
		if err := db.Collection(mcoGuestData).FindOne(ctx, bson.D{{Key: "_id", Value: "synthetic-vm"}}).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if raw.Lookup("entry").Type != bson.TypeEmbeddedDocument || raw.Lookup("updated_at").DateTime() != stamp.UnixMilli() {
			t.Fatal("saved envelope or TTL timestamp changed")
		}
		if err := b.Close(ctx); err != nil {
			t.Fatal(err)
		}
		data, err = b.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(data.GuestData["synthetic-vm"], want) {
			t.Fatal("guest telemetry changed after reconnect")
		}
	}
	db, err := b.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection(mcoGuestData).UpdateOne(ctx, bson.D{{Key: "_id", Value: "synthetic-vm"}}, bson.D{{Key: "$set", Value: bson.D{{Key: "entry", Value: int32(7)}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Load(ctx); err == nil {
		t.Fatal("malformed nested payload silently discarded")
	}
}
