package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"kvm-center/internal/models"
	"strings"
	"sync"
	"testing"
	"time"
)

func tokenFixture(t *testing.T, id string) (bson.Raw, string) {
	t.Helper()
	secret := strings.Repeat("a", 48)
	sum := sha256.Sum256([]byte(secret))
	raw, err := bson.Marshal(bson.D{{Key: "_id", Value: id}, {Key: "id", Value: id}, {Key: "name", Value: "Synthetic legacy token"}, {Key: "ownerid", Value: "fixture-reader"}, {Key: "hash", Value: hex.EncodeToString(sum[:])}, {Key: "createdat", Value: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}, {Key: "lastusedat", Value: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}, {Key: "opaque", Value: primitive.NewDecimal128(97, 101)}})
	if err != nil {
		t.Fatal(err)
	}
	return raw, tokenPrefix + id + "." + secret
}
func TestTokenPreservationWire(t *testing.T) {
	raw, bearer := tokenFixture(t, "12345678")
	if err := validateTokenBSON(raw); err != nil {
		t.Fatal(err)
	}
	var token models.APIToken
	if err := bson.Unmarshal(raw, &token); err != nil {
		t.Fatal(err)
	}
	token.Kind = "system"
	token.Revoked = true
	d := &persistData{APITokens: map[string]*models.APIToken{token.ID: &token}, EntityOverlay: &entityOverlay{Version: 6, Collections: map[string]map[string][]byte{mcoAPITokens: {token.ID: raw}}}}
	wire, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wire, []byte(bearer)) {
		t.Fatal("bearer persisted")
	}
	var back persistData
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatal(err)
	}
	got := bson.Raw(back.EntityOverlay.Collections[mcoAPITokens][token.ID])
	if !equalRaw(got.Lookup("opaque"), raw.Lookup("opaque")) || !back.APITokens[token.ID].Revoked {
		t.Fatal("token recovery lost values")
	}
	if json.Unmarshal(bytes.Replace(wire, []byte(`"version":6`), []byte(`"version":5`), 1), &back) == nil {
		t.Fatal("old overlay accepted tokens")
	}
	for _, value := range []map[string]*models.APIToken{{"nil": nil}, {"wrong": &token}} {
		if _, err := json.Marshal(&persistData{APITokens: value}); err == nil {
			t.Fatal("invalid token snapshot accepted")
		}
	}
}
func TestTokenStrictAdmission(t *testing.T) {
	raw, _ := tokenFixture(t, "12345678")
	for _, key := range []string{"_id", "owner_id", "expires_at", "last_used_at", "created_at", "OwnerID", "hash", "kind", "scopes", "revoked", "expiresat", "id"} {
		t.Run(key, func(t *testing.T) {
			var doc bson.D
			bson.Unmarshal(raw, &doc)
			value := any("conflict")
			switch key {
			case "hash":
				value = "malformed"
			case "kind":
				value = "future"
			case "scopes":
				value = bson.A{" host.view "}
			case "revoked":
				value = "false"
			case "expiresat":
				value = nil
			}
			// Replace ordinary fields; id intentionally duplicates an existing field.
			replaced := false
			for i := range doc {
				if doc[i].Key == key && key != "id" {
					doc[i].Value = value
					replaced = true
				}
			}
			if !replaced {
				doc = append(doc, bson.E{Key: key, Value: value})
			}
			bad, _ := bson.Marshal(doc)
			if validateTokenBSON(bad) == nil {
				t.Fatal("unsupported token accepted")
			}
		})
	}
}
func TestTokenCopiesAndConcurrentRevocation(t *testing.T) {
	s := newTestStore(t)
	bearer, created, err := s.CreateAPIToken("synthetic", "u1", "user", time.Time{}, []string{"host.view"})
	if err != nil {
		t.Fatal(err)
	}
	created.Scopes[0] = "user.manage"
	listed := s.ListAPITokens()
	listed[0].Scopes[0] = "user.manage"
	_, record := s.LookupAPITokenWithRecord(bearer)
	record.Scopes[0] = "user.manage"
	if s.GetAPIToken(created.ID).Scopes[0] != "host.view" {
		t.Fatal("scope alias escaped")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.LookupAPITokenWithRecord(bearer)
				s.GetAPIToken(created.ID)
			}
		}()
	}
	s.RevokeAPIToken(created.ID)
	wg.Wait()
	if s.LookupAPIToken(bearer) != nil {
		t.Fatal("revoked token accepted")
	}
}
func TestTokenCapacityRefusesBeforeMutation(t *testing.T) {
	s := newTestStore(t)
	s.saveWG.Wait()
	s.mu.Lock()
	for i := 0; i < tokenAdmissionMax; i++ {
		id := fmt.Sprintf("%08x", i)
		s.apiTokens[id] = &models.APIToken{ID: id}
	}
	s.mu.Unlock()
	if _, _, err := s.CreateAPIToken("overflow", "u1", "user", time.Time{}, nil); err == nil {
		t.Fatal("capacity accepted")
	}
	if len(s.apiTokens) != tokenAdmissionMax {
		t.Fatal("refusal mutated records")
	}
}

func TestTokenAggregateCapacityRefusesWithoutPoisoning(t *testing.T) {
	s := NewStoreWithBackend(nil, t.TempDir()+"/state.json")
	s.mu.Lock()
	// Fill the aggregate overlay allowance using ordinary valid groups.
	d := s.persistDataLocked()
	s.entityOverlay = &entityOverlay{Version: 6, Collections: map[string]map[string][]byte{}}
	overlay, err := reconcileOverlay(&d, s.entityOverlay)
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	count := 0
	for col, records := range overlay.Collections {
		if col != mcoAuditEvents {
			count += len(records)
		}
	}
	for i := count; i < 10000; i++ {
		id := fmt.Sprintf("group-%05d", i)
		s.groups[id] = &models.Group{ID: id}
	}
	s.mu.Unlock()
	if _, _, err := s.CreateAPIToken("overflow", "synthetic-owner", "system", time.Time{}, nil); err == nil {
		t.Fatal("aggregate overflow accepted")
	}
	if len(s.apiTokens) != 0 {
		t.Fatal("rejection mutated token map")
	}
	if _, _, err := s.snapshotBytes(); err != nil {
		t.Fatal("rejected mint poisoned backup", err)
	}
}
