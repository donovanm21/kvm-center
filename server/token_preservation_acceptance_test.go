//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryAcceptanceTokenLifecycle(t *testing.T) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("current artifact required")
	}
	b := newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	root := t.TempDir()
	s, admin := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	s.mu.Lock()
	s.users["fixture-disabled"] = &models.User{ID: "fixture-disabled", Enabled: false}
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	original, legacy := tokenFixture(t, "12345678")
	originalPath := filepath.Join(root, "original-token.bson")
	if err := os.WriteFile(originalPath, original, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Collection(mcoAPITokens).InsertOne(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	inactive := map[string]string{}
	for i, kind := range []string{"expired", "revoked", "orphan", "disabled", "owner-denied", "scoped"} {
		id := []string{"11111111", "22222222", "33333333", "44444444", "55555555", "66666666"}[i]
		raw, bearer := tokenFixture(t, id)
		var doc bson.D
		bson.Unmarshal(raw, &doc)
		switch kind {
		case "expired":
			doc = append(doc, bson.E{Key: "expiresat", Value: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)})
		case "revoked":
			doc = append(doc, bson.E{Key: "revoked", Value: true})
		case "orphan", "disabled":
			for j := range doc {
				if doc[j].Key == "ownerid" {
					doc[j].Value = "missing"
					if kind == "disabled" {
						doc[j].Value = "fixture-disabled"
					}
				}
			}
		case "owner-denied":
			doc = append(doc, bson.E{Key: "scopes", Value: bson.A{"vm.view"}})
		case "scoped":
			doc = append(doc, bson.E{Key: "scopes", Value: bson.A{"host.view"}})
		}
		if _, err := b.db.Collection(mcoAPITokens).InsertOne(t.Context(), doc); err != nil {
			t.Fatal(err)
		}
		inactive[kind] = bearer
	}
	p := startAcceptance(t, binary, root, b, admin, false)
	requestAs := func(bearer, path string, want int) {
		t.Helper()
		p.token = bearer
		p.request(t, "GET", path, nil, want)
		p.token = admin
	}
	checkAuth := func() {
		t.Helper()
		requestAs(legacy, "/api/tokens", 200)
		requestAs(inactive["scoped"], "/api/hosts", 200)
		requestAs(inactive["scoped"], "/api/tokens", 403)
		requestAs(inactive["owner-denied"], "/api/tags", 403)
		for _, kind := range []string{"expired", "revoked", "orphan", "disabled"} {
			requestAs(inactive[kind], "/api/hosts", 401)
		}
	}
	checkAuth()
	var minted struct {
		Token  string          `json:"token"`
		Record models.APIToken `json:"record"`
	}
	response := p.request(t, "POST", "/api/tokens", []byte(`{"name":"Synthetic minted","scopes":["host.view"]}`), 200)
	if err := json.Unmarshal(response, &minted); err != nil || minted.Token == "" || minted.Record.Hash != "" {
		t.Fatal("mint response invalid or hash exposed")
	}
	requestAs(minted.Token, "/api/hosts", 200)
	p.request(t, "DELETE", "/api/tokens/66666666", nil, 200)
	requestAs(inactive["scoped"], "/api/hosts", 401)
	p.stop(t, false)
	verify := func() {
		t.Helper()
		var got bson.Raw
		if err := b.db.Collection(mcoAPITokens).FindOne(t.Context(), bson.D{{Key: "_id", Value: "12345678"}}).Decode(&got); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"id", "name", "ownerid", "hash", "createdat", "opaque"} {
			if !equalRaw(got.Lookup(key), original.Lookup(key)) {
				t.Fatal("token changed", key)
			}
		}
		if got.Lookup("lastusedat").DateTime() <= original.Lookup("lastusedat").DateTime() {
			t.Fatal("usage timestamp not persisted")
		}
		disk, err := os.ReadFile(originalPath)
		if err != nil || !bytes.Equal(disk, original) {
			t.Fatal("original changed")
		}
	}
	verify()
	p = startAcceptance(t, binary, root, b, admin, false)
	requestAs(minted.Token, "/api/hosts", 200)
	requestAs(inactive["scoped"], "/api/hosts", 401)
	requestAs(legacy, "/api/tokens", 200)
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restoreRoot := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restoreRoot, 0)
	if snapshot.EntityOverlay.Version != 8 || len(snapshot.APITokens) != 9 {
		t.Fatal("token backup incomplete")
	}
	wire, _ := json.Marshal(snapshot)
	if bytes.Contains(wire, []byte(minted.Token)) || bytes.Contains(wire, []byte(legacy)) {
		t.Fatal("bearer persisted")
	}
	b = newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	p = startAcceptance(t, binary, restoreRoot, b, admin, false)
	requestAs(minted.Token, "/api/hosts", 200)
	requestAs(inactive["scoped"], "/api/hosts", 401)
	requestAs(legacy, "/api/tokens", 200)
	verify()
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	if !equalRaw(bson.Raw(second.EntityOverlay.Collections[mcoAPITokens]["12345678"]).Lookup("opaque"), original.Lookup("opaque")) {
		t.Fatal("second backup lost opaque")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restoreRoot, b, admin, false)
	requestAs(minted.Token, "/api/hosts", 200)
	requestAs(inactive["scoped"], "/api/hosts", 401)
	verify()
	p.stop(t, false)
	sum := sha256.Sum256(original)
	emitAcceptance(t, "token_lifecycle", map[string]any{"backend": "mongo", "fixture_schema": "synthetic-api-tokens-bson-v1", "original_sha256": hex.EncodeToString(sum[:]), "mint_auth_scopes_expiry_revoke_restart": true, "last_used_persisted": true, "application_backup_fresh_restore": true, "second_backup_restart": true, "opaque_preserved": true, "originals_unchanged": true, "ttl_monitor_disabled": true, "crash_durable_acknowledgement": false, "full_legacy_compatible": false})
}
