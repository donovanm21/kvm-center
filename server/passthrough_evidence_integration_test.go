//go:build integration

package main

import (
	"context"
	"go.mongodb.org/mongo-driver/bson"
	"strings"
	"testing"
)

func TestMongoIntegrationPassthroughEvidenceRecovery(t *testing.T) {
	ctx := context.Background()
	b := newMongoIntegrationBackend(t)
	d := passthroughSnapshot(t)
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	var fixture bson.D
	_ = bson.Unmarshal(d.EntityOverlay.Collections[mcoVMs][tagFixtureUUID], &fixture)
	fixture[0].Value = id
	fixture[1].Value = id
	original, _ := bson.Marshal(fixture)
	d.VMs["fixture"][0].UUID = id
	d.EntityOverlay.Collections[mcoVMs] = map[string][]byte{id: original}
	if err := b.restoreEntitySnapshot(ctx, d); err != nil {
		t.Fatal(err)
	}
	s := NewStoreWithBackend(b, "")
	s.saveWG.Wait()
	v, err := s.PassthroughEvidence(ctx, id)
	if err != nil || v.HasPassthrough == nil || !*v.HasPassthrough {
		t.Fatal(v, err)
	}
	// A second identity with a conflicting _id must not hide behind the first.
	raw := original
	var doc bson.D
	_ = bson.Unmarshal(raw, &doc)
	doc[0].Value = "conflicting-identity"
	doc[1].Value = strings.ToUpper(id)
	if _, err = b.db.Collection(mcoVMs).InsertOne(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PassthroughEvidence(ctx, id); err == nil {
		t.Fatal("duplicate VM identity accepted")
	}
	if _, err = b.db.Collection(mcoVMs).DeleteOne(ctx, bson.M{"_id": "conflicting-identity"}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.db.Collection(mcoVMs).UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"has_passthrough": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PassthroughEvidence(ctx, id); err == nil {
		t.Fatal("cached alias accepted")
	}
}
