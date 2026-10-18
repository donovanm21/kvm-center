package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"kvm-center/internal/models"
	"testing"
	"time"
)

func sessionFixture(t *testing.T, token string) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(bson.D{{Key: "_id", Value: token}, {Key: "token", Value: token}, {Key: "userid", Value: "fixture-reader"}, {Key: "createdat", Value: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}, {Key: "expiresat", Value: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}, {Key: "useragent", Value: "Synthetic legacy client"}, {Key: "opaque", Value: primitive.NewDecimal128(103, 107)}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestSessionPreservationWire(t *testing.T) {
	raw := sessionFixture(t, "original")
	if err := validateSessionBSON(raw); err != nil {
		t.Fatal(err)
	}
	var sess models.Session
	if err := bson.Unmarshal(raw, &sess); err != nil {
		t.Fatal(err)
	}
	d := &persistData{Sessions: map[string]*models.Session{sess.Token: &sess}, EntityOverlay: &entityOverlay{Version: 7, Collections: map[string]map[string][]byte{mcoSessions: {sess.Token: raw}}}}
	wire, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back persistData
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatal(err)
	}
	got := bson.Raw(back.EntityOverlay.Collections[mcoSessions][sess.Token])
	if !equalRaw(got.Lookup("opaque"), raw.Lookup("opaque")) || got.Lookup("expires_at").DateTime() != raw.Lookup("expiresat").DateTime() {
		t.Fatal("session preservation failed")
	}
	if json.Unmarshal(bytes.Replace(wire, []byte(`"version":8`), []byte(`"version":6`), 1), &back) == nil {
		t.Fatal("old overlay accepted sessions")
	}
	delete(d.Sessions, sess.Token)
	wire, err = json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.EntityOverlay.Collections[mcoSessions]) != 0 {
		t.Fatal("deleted session resurrected")
	}
	for _, sessions := range []map[string]*models.Session{{"nil": nil}, {"other": &sess}} {
		if _, err := json.Marshal(&persistData{Sessions: sessions}); err == nil {
			t.Fatal("ambiguous session accepted")
		}
	}
}
func TestSessionStrictAdmission(t *testing.T) {
	for _, kind := range []string{"identity", "owner-alias", "created-type", "expires-conflict", "expires-null", "case", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			var doc bson.D
			bson.Unmarshal(sessionFixture(t, "original"), &doc)
			switch kind {
			case "identity":
				doc[0].Value = "other"
			case "owner-alias":
				doc = append(doc, bson.E{Key: "user_id", Value: "other"})
			case "created-type":
				doc[3].Value = "2020-01-02"
			case "expires-conflict":
				doc = append(doc, bson.E{Key: "expires_at", Value: time.Date(2098, 1, 1, 0, 0, 0, 0, time.UTC)})
			case "expires-null":
				doc[4].Value = nil
			case "case":
				doc[4].Key = "EXPIRESAT"
			case "duplicate":
				doc = append(doc, bson.E{Key: "token", Value: "original"})
			}
			raw, _ := bson.Marshal(doc)
			if validateSessionBSON(raw) == nil {
				t.Fatal("unsupported session accepted")
			}
		})
	}
}
func TestSessionCopiesMutationPersistenceAndDeletion(t *testing.T) {
	path := t.TempDir() + "/state.json"
	s := NewStore(path)
	t.Cleanup(func() { s.saveWG.Wait() })
	sess, err := s.CreateSession("synthetic-owner", "test-client")
	if err != nil {
		t.Fatal(err)
	}
	token := sess.Token
	sess.UserID = "changed"
	if s.GetSession(token).UserID != "synthetic-owner" {
		t.Fatal("mutable session escaped")
	}
	s.mu.Lock()
	s.sessions[token].ExpiresAt = time.Now().Add(time.Hour)
	s.mu.Unlock()
	if s.TouchSession(token) == nil {
		t.Fatal("renewal failed")
	}
	s.saveWG.Wait()
	fresh := NewStore(path)
	t.Cleanup(func() { fresh.saveWG.Wait() })
	if got := fresh.GetSession(token); got == nil || time.Until(got.ExpiresAt) < sessionLifetime/2 {
		t.Fatal("renewal not persisted")
	}
	s.DeleteSession(token)
	s.saveWG.Wait()
	fresh = NewStore(path)
	if fresh.GetSession(token) != nil {
		t.Fatal("logout not persisted")
	}
	expired, err := s.CreateSession("synthetic-owner", "expired-client")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.sessions[expired.Token].ExpiresAt = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if s.TouchSession(expired.Token) != nil {
		t.Fatal("expired session accepted")
	}
	s.saveWG.Wait()
	fresh = NewStore(path)
	if fresh.sessions[expired.Token] != nil {
		t.Fatal("expiry deletion not persisted")
	}
	purged, err := s.CreateSession("synthetic-owner", "purge-client")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.sessions[purged.Token].ExpiresAt = time.Now().Add(-time.Second)
	s.mu.Unlock()
	s.PurgeExpiredSessions()
	s.saveWG.Wait()
	fresh = NewStore(path)
	if fresh.sessions[purged.Token] != nil {
		t.Fatal("purge not persisted")
	}
}
func TestSessionAdmissionRefusalKeepsLastLoginAndBackup(t *testing.T) {
	s := NewStoreWithBackend(nil, t.TempDir()+"/state.json")
	s.users["u"] = &models.User{ID: "u"}
	s.entityOverlay = &entityOverlay{Version: 7, Collections: map[string]map[string][]byte{}}
	count := 0
	d := s.persistDataLocked()
	for col, docs := range overlayCandidates(&d) {
		if col != mcoAuditEvents {
			count += len(docs)
		}
	}
	for i := count; i < 10000; i++ {
		id := fmt.Sprintf("group-%05d", i)
		s.groups[id] = &models.Group{ID: id}
	}
	if _, err := s.CreateSession("u", "overflow"); err == nil {
		t.Fatal("aggregate overflow accepted")
	}
	if len(s.sessions) != 0 || !s.users["u"].LastLoginAt.IsZero() {
		t.Fatal("rejected session mutated state")
	}
	if _, _, err := s.snapshotBytes(); err != nil {
		t.Fatal("rejection poisoned backup", err)
	}
}
