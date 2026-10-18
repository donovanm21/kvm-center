//go:build integration

package main

import (
	"bytes"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
	"testing"
	"time"
)

func TestMongoIntegrationAuditPreservation(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	raw := auditFixture(t, "original")
	// Avoid expiry in the ordinary Mongo profile. Original expired bytes are
	// separately exercised with a verified inactive scanner by native acceptance.
	var doc bson.D
	bson.Unmarshal(raw, &doc)
	for i := range doc {
		if doc[i].Key == "at" {
			doc[i].Value = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		}
	}
	raw, _ = bson.Marshal(doc)
	if _, err := b.db.Collection(mcoAuditEvents).InsertOne(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	d, err := b.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	d.Audit = append(d.Audit, &models.AuditEvent{ID: "new", At: time.Date(2099, 1, 2, 0, 0, 0, 0, time.UTC), Action: "synthetic"})
	if err := b.Save(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	var got bson.Raw
	if err := b.db.Collection(mcoAuditEvents).FindOne(t.Context(), bson.D{{Key: "_id", Value: "original"}}).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !equalRaw(got.Lookup("opaque"), bson.Raw(raw).Lookup("opaque")) {
		t.Fatal("append lost opaque")
	}
	overlay, err := b.captureEntityOverlay(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	d.EntityOverlay = overlay
	fresh := newMongoIntegrationBackend(t)
	if err := fresh.restoreEntitySnapshot(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	var restored bson.Raw
	fresh.db.Collection(mcoAuditEvents).FindOne(t.Context(), bson.D{{Key: "_id", Value: "original"}}).Decode(&restored)
	if !bytes.Equal(got, restored) {
		t.Fatal("fresh restore changed record")
	}
}

func TestMongoIntegrationAuditCachedAdmissionBound(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	d, err := b.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	docs := make([]any, auditRetentionMax+1)
	for i := range docs {
		id := fmt.Sprintf("synthetic-%05d", i)
		docs[i] = bson.D{{Key: "_id", Value: id}, {Key: "id", Value: id}, {Key: "at", Value: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}}
	}
	if _, err := b.db.Collection(mcoAuditEvents).InsertMany(t.Context(), docs); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(t.Context(), d); err == nil {
		t.Fatal("cached save truncated oversized audit")
	}
	if _, err := b.captureEntityOverlay(t.Context(), d); err == nil {
		t.Fatal("cached backup omitted oversized audit")
	}
	n, err := b.db.Collection(mcoAuditEvents).CountDocuments(t.Context(), bson.D{})
	if err != nil || n != auditRetentionMax+1 {
		t.Fatal("refusal changed original count")
	}
}
