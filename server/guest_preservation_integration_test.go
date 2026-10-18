//go:build integration

package main

import (
	"bytes"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/models"
	"testing"
)

func TestMongoIntegrationGuestRefusalBeforeTTL(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	db := b.db
	if _, err := db.Collection(mcoGuestData).Indexes().DropAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	original, _ := bson.Marshal(bson.D{{Key: "_id", Value: "malformed"}, {Key: "vm_uuid", Value: "malformed"}, {Key: "entry", Value: int32(7)}})
	if _, err := db.Collection(mcoGuestData).InsertOne(t.Context(), bson.Raw(original)); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Load(t.Context()); err == nil {
		t.Fatal("malformed guest boot accepted")
	}
	// Reconnect directly, without application index initialization.
	direct, err := mongo.Connect(t.Context(), options.Client().ApplyURI(b.uri))
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Disconnect(t.Context())
	col := direct.Database(b.dbName).Collection(mcoGuestData)
	cur, err := col.Indexes().List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var indexes []bson.M
	if err := cur.All(t.Context(), &indexes); err != nil {
		t.Fatal(err)
	}
	if len(indexes) != 1 || indexes[0]["name"] != "_id_" {
		t.Fatal("refusal created indexes", indexes)
	}
	var got bson.Raw
	if err := col.FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || !bytes.Equal(original, got) {
		t.Fatal("refusal changed original", err)
	}
}

func TestMongoIntegrationGuestPreflightBeforeWrites(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	original, _ := bson.Marshal(bson.D{{Key: "_id", Value: "host"}, {Key: "id", Value: "host"}, {Key: "cpuusage", Value: 1.0}})
	if _, err := b.db.Collection(mcoHosts).InsertOne(t.Context(), bson.Raw(original)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Collection(mcoGuestData).InsertOne(t.Context(), bson.D{{Key: "_id", Value: "invalid"}, {Key: "entry", Value: int32(7)}}); err != nil {
		t.Fatal(err)
	}
	d := &persistData{Hosts: map[string]*models.Host{"host": {ID: "host", CPUUsage: 99}}}
	if err := b.Save(t.Context(), d); err == nil {
		t.Fatal("invalid prior guest accepted")
	}
	var got bson.Raw
	if err := b.db.Collection(mcoHosts).FindOne(t.Context(), bson.D{}).Decode(&got); err != nil || !bytes.Equal(original, got) {
		t.Fatal("guest preflight wrote another collection", err)
	}
}
