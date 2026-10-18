//go:build integration

package main

import (
	"bytes"
	"go.mongodb.org/mongo-driver/bson"
	"testing"
	"time"
)

func TestMongoIntegrationTokenPreservation(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	raw, _ := tokenFixture(t, "12345678")
	if _, err := b.db.Collection(mcoAPITokens).InsertOne(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	d, err := b.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	d.APITokens["12345678"].LastUsedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	d.APITokens["12345678"].Revoked = true
	if err := b.Save(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	var got bson.Raw
	if err := b.db.Collection(mcoAPITokens).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !equalRaw(got.Lookup("opaque"), raw.Lookup("opaque")) {
		t.Fatal("known write lost opaque")
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
	if err := fresh.db.Collection(mcoAPITokens).FindOne(t.Context(), bson.D{}).Decode(&restored); err != nil || !bytes.Equal(got, restored) {
		t.Fatal("restore changed token", err)
	}
}
func TestMongoIntegrationTokenCachedRefusal(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	d, err := b.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := tokenFixture(t, "12345678")
	var doc bson.D
	bson.Unmarshal(raw, &doc)
	doc = append(doc, bson.E{Key: "expires_at", Value: time.Now()})
	bad, _ := bson.Marshal(doc)
	if _, err := b.db.Collection(mcoAPITokens).InsertOne(t.Context(), bson.Raw(bad)); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(t.Context(), d); err == nil {
		t.Fatal("cached save accepted ambiguous token")
	}
	if _, err := b.captureEntityOverlay(t.Context(), d); err == nil {
		t.Fatal("cached export accepted ambiguous token")
	}
	if _, err := b.Load(t.Context()); err == nil {
		t.Fatal("load accepted ambiguous token")
	}
	var got bson.Raw
	if err := b.db.Collection(mcoAPITokens).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || !bytes.Equal(got, bad) {
		t.Fatal("refusal changed original", err)
	}
}
