//go:build integration

package main

import (
	"bytes"
	"go.mongodb.org/mongo-driver/bson"
	"testing"
	"time"
)

func TestMongoIntegrationSessionPreservation(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	raw := sessionFixture(t, "original")
	if _, err := b.db.Collection(mcoSessions).InsertOne(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	d, err := b.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	d.Sessions["original"].ExpiresAt = time.Date(2099, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := b.Save(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	var got bson.Raw
	if err := b.db.Collection(mcoSessions).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !equalRaw(got.Lookup("opaque"), raw.Lookup("opaque")) {
		t.Fatal("renewal lost opaque")
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
	if err := fresh.db.Collection(mcoSessions).FindOne(t.Context(), bson.D{}).Decode(&restored); err != nil || !bytes.Equal(restored, got) {
		t.Fatal("restoration changed session", err)
	}
	d.EntityOverlay = nil
	delete(d.Sessions, "original")
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
	n, err := empty.db.Collection(mcoSessions).CountDocuments(t.Context(), bson.D{})
	if err != nil || n != 0 {
		t.Fatal("deleted session resurrected")
	}
}
func TestMongoIntegrationSessionCachedRefusal(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	d, err := b.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var doc bson.D
	bson.Unmarshal(sessionFixture(t, "original"), &doc)
	doc = append(doc, bson.E{Key: "expires_at", Value: time.Date(2098, 1, 1, 0, 0, 0, 0, time.UTC)})
	raw, _ := bson.Marshal(doc)
	if _, err := b.db.Collection(mcoSessions).InsertOne(t.Context(), bson.Raw(raw)); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(t.Context(), d); err == nil {
		t.Fatal("cached save accepted conflicting session")
	}
	if _, err := b.captureEntityOverlay(t.Context(), d); err == nil {
		t.Fatal("backup accepted conflicting session")
	}
	if _, err := b.Load(t.Context()); err == nil {
		t.Fatal("load accepted conflicting session")
	}
	var got bson.Raw
	if err := b.db.Collection(mcoSessions).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || !bytes.Equal(raw, got) {
		t.Fatal("refusal changed original", err)
	}
}
