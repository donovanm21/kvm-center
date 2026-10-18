//go:build integration

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
)

func TestMongoIntegrationTagStateRoundTrip(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	s := drainStore(b)
	s.pendingEnrollments = map[string]*models.PendingEnrollment{"fixture": {ID: "fixture", PollTokenHash: "synthetic-private"}}
	if err := s.SetVMTags(tagFixtureUUID, []string{"database", "production"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := b.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.VMTagState, tagFixture()) || loaded.PendingEnrollments["fixture"].PollTokenHash != "synthetic-private" {
		t.Fatal("Mongo reconnect lost tags/private state")
	}
	raw, err := s.ExportSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot persistData
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	destination := newMongoIntegrationBackend(t)
	if err := destination.restoreEntitySnapshot(ctx, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackendRoundTrip(ctx, &snapshot, destination); err != nil {
		t.Fatal(err)
	}
	// Removing the singleton in this isolated fixture proves an unchanged save
	// does not rewrite a clean tag collection. Outside writers are unsupported.
	db, err := b.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection(mcoVMTagState).DeleteMany(ctx, bson.D{}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	count, err := db.Collection(mcoVMTagState).CountDocuments(ctx, bson.D{})
	if err != nil || count != 0 {
		t.Fatal("unchanged tags rewritten")
	}
	if err := s.SetVMTags(tagFixtureUUID, []string{"changed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVMTags(tagFixtureUUID, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	cleared, err := b.Load(ctx)
	if err != nil || cleared.VMTagState != nil {
		t.Fatal("cleared tags resurrected after reconnect")
	}
	// Explicit empty envelope is also a clear, not a skipped singleton write.
	if err := destination.Save(ctx, &persistData{VMTagState: &vmTagState{Version: 1, ByVM: map[string][]string{}}}); err != nil {
		t.Fatal(err)
	}
	empty, err := destination.Load(ctx)
	if err != nil || empty.VMTagState != nil {
		t.Fatal("empty envelope did not clear")
	}
}
func TestMongoIntegrationTagStateCompatibilityGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, kind := range []string{"version", "unknown", "duplicate", "null", "extra-document", "legacy", "outer-field", "identity"} {
		t.Run(kind, func(t *testing.T) {
			b := newMongoIntegrationBackend(t)
			db, err := b.dial(ctx)
			if err != nil {
				t.Fatal(err)
			}
			state := bson.D{{Key: "version", Value: 1}, {Key: "by_vm", Value: bson.M{}}}
			collection := mcoVMTagState
			switch kind {
			case "version":
				state[0].Value = 99
			case "unknown":
				state = append(state, bson.E{Key: "future", Value: true})
			case "duplicate":
				state = append(state, bson.E{Key: "version", Value: 1})
			case "legacy":
				collection = "vm_tags"
			}
			var payload any = state
			if kind == "null" {
				payload = nil
			}
			document := bson.M{"_id": "singleton", "state": payload}
			if kind == "outer-field" {
				document["future"] = true
			}
			if kind == "identity" {
				document["_id"] = "unexpected"
			}
			if _, err := db.Collection(collection).InsertOne(ctx, document); err != nil {
				t.Fatal(err)
			}
			if kind == "extra-document" {
				if _, err := db.Collection(collection).InsertOne(ctx, bson.M{"_id": "unexpected", "state": state}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := db.Collection(collection).CountDocuments(ctx, bson.D{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.Load(ctx); err == nil {
				t.Fatal("unsupported tag namespace loaded")
			}
			s := NewStoreWithBackend(b, filepath.Join(t.TempDir(), "state.json"))
			s.saveWG.Wait()
			if err := s.SaveChecked(); err == nil {
				t.Fatal("unsupported load opened persistence gate")
			}
			// Direct backend migration writes must also reject the namespace before
			// touching any otherwise-valid collection.
			if err := b.Save(ctx, &persistData{VMTagState: tagFixture(), Hosts: map[string]*models.Host{"should-not-write": {ID: "should-not-write"}}}); err == nil {
				t.Fatal("direct save overwrote incompatible namespace")
			}
			after, err := db.Collection(collection).CountDocuments(ctx, bson.D{})
			if err != nil || after != before {
				t.Fatal("unsupported documents changed")
			}
			count, err := db.Collection(mcoHosts).CountDocuments(ctx, bson.D{})
			if err != nil || count != 0 {
				t.Fatal("compatibility failure wrote other collections")
			}
		})
	}
}
func TestMongoIntegrationTagStateOlderWriterVisibility(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := b.Save(ctx, &persistData{VMTagState: tagFixture()}); err != nil {
		t.Fatal(err)
	}
	db, err := b.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Representative old Mongo writer only replaces collections it knows.
	if err := replaceMap(ctx, db, mcoHosts, nil); err != nil {
		t.Fatal(err)
	}
	got, err := b.Load(ctx)
	if err != nil || !reflect.DeepEqual(got.VMTagState, tagFixture()) {
		t.Fatal("unrecognized collection did not remain stored")
	}
	// This is retention, not downgrade support: an older reader/export omits it.
}

// Uses actual tag route/permission handling and real Mongo, then reloads a fresh
// Store after disconnect. Browser page reload alone cannot prove this property.
func TestMongoIntegrationTagAPIWorkflow(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	s := NewStoreWithBackend(b, filepath.Join(t.TempDir(), "unused.json"))
	t.Cleanup(func() { s.saveWG.Wait() })
	u := tagActor(s)
	for _, body := range []string{tagCreateBody, `{"tags":["edited"],"expected_tags":["database","production"]}`} {
		if w := tagRequest(s, u, "PUT", body); w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	s.saveWG.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	loaded := NewStoreWithBackend(b, filepath.Join(t.TempDir(), "unused.json"))
	t.Cleanup(func() { loaded.saveWG.Wait() })
	if !reflect.DeepEqual(loaded.GetVMTags(tagFixtureUUID), []string{"edited"}) {
		t.Fatal("API edit lost after Mongo reconnect")
	}
	if w := tagRequest(loaded, u, "PUT", `{"tags":[],"expected_tags":["edited"]}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	loaded.saveWG.Wait()
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	cleared, err := b.Load(ctx)
	if err != nil || cleared.VMTagState != nil {
		t.Fatal("API clear resurrected after Mongo reconnect")
	}
}
