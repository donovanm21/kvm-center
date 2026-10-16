//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"kvm-center/internal/models"
	"kvm-center/internal/validationfixture"
)

// This is deliberately opt-in and fails (never skips) if the fixture is absent.
// It creates and drops ONLY a random database on an unauthenticated loopback
// MongoDB. Never point this suite at a shared/production Mongo instance.
func newMongoIntegrationBackend(t *testing.T) *MongoBackend {
	t.Helper()
	uri, err := validationfixture.MongoURI(os.Getenv("KVMC_TEST_MONGO_URI"))
	if err != nil {
		t.Fatal(err)
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	b := NewMongoBackend(uri, "kvmc_validation_"+hex.EncodeToString(random[:]))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := b.dial(ctx); err != nil {
		t.Fatalf("connect disposable MongoDB: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		db, err := b.dial(ctx)
		if err != nil {
			t.Errorf("reconnect to clean test database %s: %v", b.dbName, err)
		} else if err := db.Drop(ctx); err != nil {
			t.Errorf("clean test database %s: %v", b.dbName, err)
		}
		if err := b.Close(ctx); err != nil {
			t.Errorf("close test MongoDB: %v", err)
		}
	})
	return b
}

func TestMongoIntegrationRoundTrip(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	data := &persistData{
		Hosts:              map[string]*models.Host{"host-one": {ID: "host-one"}},
		VMs:                map[string][]*models.VM{"host-one": {{UUID: "vm-one", HostID: "host-one", Name: "fixture-vm"}}},
		Users:              map[string]*models.User{"user-one": {ID: "user-one", Username: "fixture-user", PasswordHash: "fixture-hash", Enabled: true}},
		Sessions:           map[string]*models.Session{"session-one": {Token: "session-one", UserID: "user-one", ExpiresAt: time.Now().Add(time.Hour)}},
		APITokens:          map[string]*models.APIToken{"token-one": {ID: "token-one", OwnerID: "user-one", Hash: "fixture-token-hash", Scopes: []string{"vm.view"}}},
		PendingEnrollments: map[string]*models.PendingEnrollment{"enroll-one": {ID: "enroll-one", HostID: "host-one", PollTokenHash: "fixture-poll-hash", IssuedCertPEM: "fixture-cert", IssuedCAPEM: "fixture-ca", ExpiresAt: time.Now().Add(time.Hour)}},
		DiscordGateway:     &models.DiscordGateway{Enabled: true, Token: "fixture-gateway-token"},
		Features:           &models.Features{DisableVMCreate: true},
		ConsoleSecret:      "fixture-console-secret",
	}
	if err := b.Save(ctx, data); err != nil {
		t.Fatal(err)
	}
	// Force a fresh connection/load, rather than checking an in-memory snapshot.
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := b.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hosts) != 1 || len(got.VMs["host-one"]) != 1 || got.VMs["host-one"][0].UUID != "vm-one" {
		t.Fatal("inventory did not survive Mongo reconnect")
	}
	if user := got.Users["user-one"]; user == nil || user.PasswordHash != "fixture-hash" || !user.Enabled {
		t.Fatal("user authentication state did not round-trip")
	}
	if session := got.Sessions["session-one"]; session == nil || session.UserID != "user-one" {
		t.Fatal("session did not round-trip")
	}
	if token := got.APITokens["token-one"]; token == nil || token.Hash != "fixture-token-hash" || !token.AllowsPermission("vm.view") || token.AllowsPermission("vm.delete") {
		t.Fatal("token hash/scope did not round-trip")
	}
	if enrol := got.PendingEnrollments["enroll-one"]; enrol == nil || enrol.PollTokenHash != "fixture-poll-hash" || enrol.IssuedCertPEM != "fixture-cert" || enrol.IssuedCAPEM != "fixture-ca" {
		t.Fatal("BSON-only enrollment fields did not round-trip")
	}
	if got.DiscordGateway == nil || got.DiscordGateway.Token != "fixture-gateway-token" || got.ConsoleSecret != "fixture-console-secret" {
		t.Fatal("private gateway/console state did not round-trip")
	}
	if got.Features == nil || !got.Features.DisableVMCreate {
		t.Fatal("feature toggle did not round-trip")
	}

	// Empty collections and false booleans must persist (no resurrection).
	data.Hosts = map[string]*models.Host{}
	data.VMs = map[string][]*models.VM{}
	data.Users = map[string]*models.User{}
	data.Sessions = map[string]*models.Session{}
	data.APITokens = map[string]*models.APIToken{}
	data.PendingEnrollments = map[string]*models.PendingEnrollment{}
	data.Features.DisableVMCreate = false
	if err := b.Save(ctx, data); err != nil {
		t.Fatal(err)
	}
	got, err = b.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hosts)+len(got.VMs)+len(got.Users)+len(got.Sessions)+len(got.APITokens)+len(got.PendingEnrollments) != 0 {
		t.Fatal("removed inventory or auth records reappeared after save")
	}
	if got.Features == nil || got.Features.DisableVMCreate {
		t.Fatal("false feature toggle was not persisted")
	}
}

func TestMongoIntegrationDirtyCollections(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	data := &persistData{Users: map[string]*models.User{"u": {ID: "u", Username: "before", Enabled: true}}}
	if err := b.Save(ctx, data); err != nil {
		t.Fatal(err)
	}
	data.Users["u"].Username = "after"
	data.Dirty = map[string]bool{} // clean collections must remain untouched
	if err := b.Save(ctx, data); err != nil {
		t.Fatal(err)
	}
	got, err := b.Load(ctx)
	if err != nil || got.Users["u"] == nil || got.Users["u"].Username != "before" {
		t.Fatalf("clean users collection was replaced (load error: %v)", err)
	}
	data.Dirty["users"] = true
	if err := b.Save(ctx, data); err != nil {
		t.Fatal(err)
	}
	got, err = b.Load(ctx)
	if err != nil || got.Users["u"] == nil || got.Users["u"].Username != "after" {
		t.Fatalf("dirty users collection was not replaced (load error: %v)", err)
	}
}

// Retained records are replaced in place, stale records removed, and new
// records inserted. An invalid plan must leave the existing collection intact.
func TestMongoIntegrationReplacementCompatibility(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := b.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const collection = "replacement_fixture"
	if err := replaceMap(ctx, db, collection, []any{
		bson.M{"_id": "keep", "value": "old"}, bson.M{"_id": "remove", "value": "old"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := replaceMap(ctx, db, collection, []any{
		bson.M{"_id": "keep", "value": "new"}, bson.M{"_id": "add", "value": "new"},
	}); err != nil {
		t.Fatal(err)
	}
	// A valid first document followed by an invalid one must cause no writes.
	if err := replaceMap(ctx, db, collection, []any{
		bson.M{"_id": "keep", "value": "incorrect"}, bson.M{"value": "missing id"},
	}); err == nil {
		t.Fatal("invalid replacement plan accepted")
	}
	cursor, err := db.Collection(collection).Find(ctx, bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.Close(ctx)
	var docs []bson.M
	if err := cursor.All(ctx, &docs); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, doc := range docs {
		got[doc["_id"].(string)] = doc["value"].(string)
	}
	if len(got) != 2 || got["keep"] != "new" || got["add"] != "new" {
		t.Fatalf("replacement or invalid-plan handling changed retained records: %v", got)
	}
	if err := replaceMap(ctx, db, collection, nil); err != nil {
		t.Fatal(err)
	}
	count, err := db.Collection(collection).CountDocuments(ctx, bson.D{})
	if err != nil || count != 0 {
		t.Fatalf("empty snapshot did not clear collection: count=%d err=%v", count, err)
	}
}

// A database-side error after an earlier replacement succeeds must not run
// the pruning step. Retrying the full snapshot must converge without duplicates.
func TestMongoIntegrationReplacementPartialFailureRetry(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := b.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const collection = "replacement_retry_fixture"
	coll := db.Collection(collection)
	if _, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "slot", Value: 1}}, Options: options.Index().SetUnique(true),
	}); err != nil {
		t.Fatal(err)
	}
	doc := func(id, slot, value string) any { return bson.M{"_id": id, "slot": slot, "value": value} }
	if err := replaceMap(ctx, db, collection, []any{
		doc("keep-a", "a", "old"), doc("keep-b", "b", "old"), doc("stale", "c", "old"),
	}); err != nil {
		t.Fatal(err)
	}
	// The first write succeeds; the second conflicts with keep-a's unique slot.
	// No production failpoints or external services are involved.
	err = replaceMap(ctx, db, collection, []any{
		doc("keep-a", "a", "new"), doc("keep-b", "a", "new"),
	})
	if !mongo.IsDuplicateKeyError(err) {
		t.Fatalf("want duplicate-key failure, got %v", err)
	}
	read := func() map[string]string {
		t.Helper()
		cursor, err := coll.Find(ctx, bson.D{})
		if err != nil {
			t.Fatal(err)
		}
		defer cursor.Close(ctx)
		var rows []struct {
			ID    string `bson:"_id"`
			Value string `bson:"value"`
		}
		if err := cursor.All(ctx, &rows); err != nil {
			t.Fatal(err)
		}
		result := map[string]string{}
		for _, row := range rows {
			result[row.ID] = row.Value
		}
		return result
	}
	got := read()
	if len(got) != 3 || got["keep-a"] != "new" || got["keep-b"] != "old" || got["stale"] != "old" {
		t.Fatalf("partial write removed records or continued after failure: %v", got)
	}
	desired := []any{doc("keep-a", "a", "new"), doc("keep-b", "b", "new"), doc("added", "d", "new")}
	for attempt := 0; attempt < 2; attempt++ {
		if err := replaceMap(ctx, db, collection, desired); err != nil {
			t.Fatal(err)
		}
		got = read()
		if len(got) != 3 || got["keep-a"] != "new" || got["keep-b"] != "new" || got["added"] != "new" {
			t.Fatalf("retry %d did not converge idempotently: %v", attempt, got)
		}
	}
}

// Deleting and recreating a username between saves must not strand every
// future save behind the obsolete user's unique-index entry. Uses the actual
// users collection and indexes installed by newMongoIntegrationBackend.
func TestMongoIntegrationReplacementUsernameHandoff(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	before := &persistData{Users: map[string]*models.User{
		"old-user": {ID: "old-user", Username: "fixture-user", Enabled: true},
	}}
	if err := b.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	after := &persistData{Users: map[string]*models.User{
		"new-user": {ID: "new-user", Username: "fixture-user", Enabled: true},
	}, Features: &models.Features{DisableVMCreate: true}}
	for attempt := 0; attempt < 2; attempt++ {
		if err := b.Save(ctx, after); err != nil {
			t.Fatalf("username handoff/retry %d failed: %v", attempt, err)
		}
		got, err := b.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Users) != 1 || got.Users["new-user"] == nil || got.Users["new-user"].Username != "fixture-user" {
			t.Fatalf("username handoff/retry %d did not retain only the new identity", attempt)
		}
		if got.Features == nil || !got.Features.DisableVMCreate {
			t.Fatal("collection after users was not saved")
		}
	}
}

func TestMongoIntegrationUsernameConflictPreservesRetainedIDs(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	before := &persistData{Users: map[string]*models.User{
		"a": {ID: "a", Username: "fixture-a", Enabled: true},
		"b": {ID: "b", Username: "fixture-b", Enabled: true},
	}}
	if err := b.Save(ctx, before); err != nil {
		t.Fatal(err)
	}
	// This invalid desired state must fail, never delete the retained owner.
	after := &persistData{Users: map[string]*models.User{
		"a": {ID: "a", Username: "fixture-a", Enabled: true},
		"b": {ID: "b", Username: "fixture-a", Enabled: true},
	}}
	if err := b.Save(ctx, after); !mongo.IsDuplicateKeyError(err) {
		t.Fatalf("want duplicate-key failure for retained ownership conflict: %v", err)
	}
	got, err := b.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Users) != 2 || got.Users["a"] == nil || got.Users["b"] == nil ||
		got.Users["a"].Username != "fixture-a" || got.Users["b"].Username != "fixture-b" {
		t.Fatal("username conflict cleanup changed a retained identity")
	}
}
