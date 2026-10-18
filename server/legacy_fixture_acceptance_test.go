//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/offlinestaging"
)

func TestHistoryAcceptanceLegacyFixtureRefusal(t *testing.T) {
	testLegacyFixtureRefusal(t, legacyfixture.Generate(), "legacy_refusal", "legacy perf_history collection requires explicit offline conversion")
}

func TestHistoryAcceptanceDeployedLegacyFixtureRefusal(t *testing.T) {
	testLegacyFixtureRefusal(t, legacyfixture.GenerateDeployed(), "deployed_legacy_refusal", "legacy perf_history collection requires explicit offline conversion")
}

func testLegacyFixtureRefusal(t *testing.T, fixture legacyfixture.Bundle, kind, expectedReason string) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("pinned current binary required")
	}
	b := newMongoIntegrationBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Drop only the empty owned database before seeding; never activate TTL on
	// the preserved full legacy fixture or its isolated runtime copy.
	if err := b.db.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	cli, err := mongo.Connect(ctx, options.Client().ApplyURI(b.uri))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Disconnect(ctx)
	db := cli.Database(b.dbName)
	root := t.TempDir()
	src := filepath.Join(root, "originals")
	if err := os.Mkdir(src, 0700); err != nil {
		t.Fatal(err)
	}
	if err := legacyfixture.Write(src, fixture); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "stage")
	if _, err := offlinestaging.Convert(src, stage); err != nil {
		t.Fatal(err)
	}
	for name := range fixture.Documents {
		raw, err := os.ReadFile(filepath.Join(stage, "normalized-"+name+".bson"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Collection(name).InsertOne(ctx, bson.Raw(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Load(ctx); err == nil || !strings.Contains(err.Error(), expectedReason) {
		t.Fatalf("backend did not identify legacy representation: %v", err)
	}
	runCtx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	cmd := exec.CommandContext(runCtx, binary, "-no-tls", "-addr", "127.0.0.1:0", "-tls-addr", "127.0.0.1:0", "-grpc-addr", "", "-data", filepath.Join(root, "runtime", "state.json"), "-ca-dir", filepath.Join(root, "runtime", "ca"), "-tls-dir", filepath.Join(root, "runtime", "tls"), "-mongo-url", b.uri, "-mongo-db", b.dbName)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "KVM_CENTER_") && !strings.HasPrefix(v, "KVMC_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if err == nil || runCtx.Err() != nil || !bytes.Contains(output, []byte("Mongo readiness failed within startup retry budget; JSON fallback disabled")) {
		t.Fatalf("current artifact did not refuse before startup: %v", err)
	}
	names, err := db.ListCollectionNames(ctx, bson.D{})
	if err != nil || len(names) != len(fixture.Documents) {
		t.Fatal("runtime changed collections")
	}
	for name, original := range fixture.Documents {
		var actual bson.Raw
		if err := db.Collection(name).FindOne(ctx, bson.D{}).Decode(&actual); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, original) {
			t.Fatalf("runtime changed %s", name)
		}
		count, err := db.Collection(name).CountDocuments(ctx, bson.D{})
		if err != nil || count != 1 {
			t.Fatalf("runtime changed count %s", name)
		}
		indexes, err := db.Collection(name).Indexes().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var all []bson.M
		if err := indexes.All(ctx, &all); err != nil {
			t.Fatal(err)
		}
		if len(all) != 1 || all[0]["name"] != "_id_" {
			t.Fatalf("runtime activated indexes %s", name)
		}
		retained, err := os.ReadFile(filepath.Join(src, name+".bson"))
		if err != nil || !bytes.Equal(retained, original) {
			t.Fatalf("original changed %s", name)
		}
	}
	if _, err := offlinestaging.Verify(stage); err != nil {
		t.Fatal(err)
	}
	emitAcceptance(t, kind, map[string]any{"backend": "mongo", "schema": fixture.Schema, "source": fixture.Source, "current_artifact_refused": true, "collections": len(fixture.Documents), "original_checksums": fixture.Checksums, "indexes_unchanged": true, "originals_unchanged": true, "active_compatible": false, "legacy_rollback_verified": false})
}
