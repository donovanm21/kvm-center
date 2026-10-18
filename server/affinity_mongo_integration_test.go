//go:build integration

package main

import (
	"context"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/affinitypolicy"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMongoIntegrationAffinityRecovery(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx := context.Background()
	state := affinityFixtureState(t)
	d := &persistData{Affinity: state}
	if err := b.restoreEntitySnapshot(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := b.Load(ctx)
	if err != nil || !reflect.DeepEqual(got.Affinity, state) {
		t.Fatal("originals lost", err)
	}
	got.Affinity.Version = 2
	got.Affinity.ReservationPhase = "prepared"
	got.Affinity.Reservation = &affinitypolicy.Reservation{RequestID: "pending", UUID: legacyfixture.VMID, Name: "one", Source: "source", Destination: "target", DestinationAddress: "192.0.2.3"}
	if err = b.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	got.Affinity.ReservationPhase = "dispatch_possible"
	if err = b.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err = b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := b.Load(ctx)
	if err != nil || !reflect.DeepEqual(again.Affinity, got.Affinity) {
		t.Fatal("reconnect lost reservation", err)
	}
	restore := newMongoIntegrationBackend(t)
	if err = restore.restoreEntitySnapshot(ctx, again); err != nil {
		t.Fatal(err)
	}
	if err = verifyBackendRoundTrip(ctx, again, restore); err != nil {
		t.Fatal(err)
	}
	removed := *again
	removed.Affinity = nil
	if err = b.Save(ctx, &removed); err == nil {
		t.Fatal("removed originals allowed")
	}
	cleared := *again
	cleared.Affinity = again.Affinity.Clone()
	cleared.Affinity.Reservation = nil
	if err = b.Save(ctx, &cleared); err == nil {
		t.Fatal("cleared reservation allowed")
	}
	db, err := b.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Collection("affinity_state").DeleteMany(ctx, bson.D{}); err != nil {
		t.Fatal(err)
	}
	if err = b.Save(ctx, again); err == nil {
		t.Fatal("cached deletion silently repaired")
	}
}
func TestMongoIntegrationAffinityRefusalBeforeWrites(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx := context.Background()
	db, err := b.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Collection("affinity_state").InsertOne(ctx, bson.D{{Key: "_id", Value: "singleton"}, {Key: "payload", Value: `{"version":99}`}}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Load(ctx); err == nil {
		t.Fatal("unsupported affinity loaded")
	}
	if err = b.Save(ctx, &persistData{}); err == nil {
		t.Fatal("unsupported affinity overwritten")
	}
	if _, err = db.Collection("affinity_state").DeleteMany(ctx, bson.D{}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Collection("affinity_state").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "expires", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Load(ctx); err == nil {
		t.Fatal("TTL affinity index accepted")
	}
}

func TestMongoIntegrationAffinityOnlyBootstrap(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	d := &persistData{Affinity: affinityFixtureState(t), Hosts: map[string]*models.Host{"source": {ID: "source"}}}
	if err := NewJSONBackend(path).Save(ctx, d); err != nil {
		t.Fatal(err)
	}
	target := newMongoIntegrationBackend(t)
	b, err := bootstrapBackendWith(ctx, "configured", path, func() Backend { return target }, bootNoDelay)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Load(ctx)
	if err != nil || !reflect.DeepEqual(got.Affinity, d.Affinity) {
		t.Fatal("affinity-only bootstrap lost state", err)
	}
	second := filepath.Join(t.TempDir(), "state.json")
	if err := NewJSONBackend(second).Save(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrapBackendWith(ctx, "configured", second, func() Backend { return target }, bootNoDelay); err == nil {
		t.Fatal("nonempty affinity restore allowed")
	}
}

func TestMongoIntegrationAffinityPreparedAbortLedger(t *testing.T) {
	ctx := context.Background()
	b := newMongoIntegrationBackend(t)
	d := &persistData{Affinity: affinityFixtureState(t)}
	if err := b.restoreEntitySnapshot(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Affinity.Version = 2
	q := affinityReservationFor("aborted-first")
	d.Affinity.Reservation = &q
	d.Affinity.ReservationPhase = "prepared"
	if err := b.Save(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Affinity.Reservation = nil
	d.Affinity.ReservationPhase = ""
	d.Affinity.Aborted = map[string]affinitypolicy.Reservation{q.RequestID: q}
	if err := b.Save(ctx, d); err != nil {
		t.Fatal(err)
	}
	restored := newMongoIntegrationBackend(t)
	if err := restored.restoreEntitySnapshot(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := restored.Load(ctx)
	if err != nil || !reflect.DeepEqual(got.Affinity, d.Affinity) {
		t.Fatal("aborted identity lost", err)
	}
	removed := *got
	removed.Affinity = got.Affinity.Clone()
	removed.Affinity.Aborted = nil
	if err = restored.Save(ctx, &removed); err == nil {
		t.Fatal("aborted identity removed")
	}
	q2 := q
	q2.RequestID = "second"
	got.Affinity.Reservation = &q2
	got.Affinity.ReservationPhase = "prepared"
	if err = restored.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	got.Affinity.ReservationPhase = "dispatch_possible"
	if err = restored.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	got.Affinity.Reservation = nil
	got.Affinity.ReservationPhase = ""
	got.Affinity.Aborted[q2.RequestID] = q2
	if err = restored.Save(ctx, got); err == nil {
		t.Fatal("dispatched reservation aborted")
	}
}
