//go:build integration

package main

import (
	"context"
	"go.mongodb.org/mongo-driver/bson"
	"testing"
)

func TestMongoIntegrationPassthroughEvidenceRecovery(t *testing.T) {
	ctx := context.Background()
	b := newMongoIntegrationBackend(t)
	d := passthroughSnapshot(t)
	if err := b.restoreEntitySnapshot(ctx, d); err != nil {
		t.Fatal(err)
	}
	s := NewStoreWithBackend(b, "")
	s.saveWG.Wait()
	v, err := s.PassthroughEvidence(ctx, tagFixtureUUID)
	if err != nil || v.HasPassthrough == nil || !*v.HasPassthrough {
		t.Fatal(v, err)
	}
	// A second identity with a conflicting _id must not hide behind the first.
	raw := passthroughFixture(t)
	var doc bson.D
	_ = bson.Unmarshal(raw, &doc)
	doc[0].Value = "conflicting-identity"
	if _, err = b.db.Collection(mcoVMs).InsertOne(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PassthroughEvidence(ctx, tagFixtureUUID); err == nil {
		t.Fatal("duplicate VM identity accepted")
	}
	if _, err = b.db.Collection(mcoVMs).DeleteOne(ctx, bson.M{"_id": "conflicting-identity"}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.db.Collection(mcoVMs).UpdateOne(ctx, bson.M{"_id": tagFixtureUUID}, bson.M{"$set": bson.M{"has_passthrough": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PassthroughEvidence(ctx, tagFixtureUUID); err == nil {
		t.Fatal("cached alias accepted")
	}
}
