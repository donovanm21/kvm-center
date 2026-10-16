//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

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
