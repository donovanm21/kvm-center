//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
)

func TestMongoIntegrationHistoryTelemetryAPIReload(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	s := NewStoreWithBackend(b, filepath.Join(t.TempDir(), "unused.json"))
	t.Cleanup(func() { s.saveWG.Wait() })
	s.mu.Lock()
	s.historyEnabled = true
	s.users["history-reader"] = &models.User{ID: "history-reader", Enabled: true, RoleIDs: []string{"history-role"}}
	s.roles["history-role"] = &models.Role{ID: "history-role", Permissions: []string{"host.view"}}
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("history-fixture", "history-reader", "user", time.Time{}, []string{"host.view"})
	if err != nil {
		t.Fatal(err)
	}
	s.ApplyTelemetry(&models.TelemetryPayload{HostID: "fixture", Timestamp: time.Unix(1, 0), CPUUsage: 22.5, MemUsage: 50})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.work.seal(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	loaded := NewStoreWithBackend(b, filepath.Join(t.TempDir(), "unused.json"))
	t.Cleanup(func() { loaded.saveWG.Wait() })
	srv := httptest.NewServer(authMiddleware(loaded, requirePerm(loaded, "host.view", handleHostHistory(loaded))))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/hosts/fixture/history?range=48h", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var got struct {
		Points  []historyViewPoint `json:"points"`
		Enabled bool               `json:"collection_enabled"`
	}
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || got.Enabled || len(got.Points) != 1 || got.Points[0].CPUAvg != 22.5 || got.Points[0].N != 1 {
		t.Fatalf("HTTP API lost persisted telemetry: %+v status %d", got, response.StatusCode)
	}
	raw, err := loaded.ExportSnapshot()
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
	if err := loaded.SaveChecked(); err != nil {
		t.Fatal(err)
	} // collection disabled, state preserved
	loaded.mu.Lock()
	pruneHistory(loaded.history, time.Now().Unix()+historyMaxPoints*300)
	loaded.mu.Unlock()
	if err := loaded.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	loaded.saveWG.Wait()
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	cleared, err := b.Load(ctx)
	if err != nil || cleared.HostHistory != nil {
		t.Fatal("expired history resurrected after reconnect")
	}
}
func TestMongoIntegrationHistoryCompatibilityGuard(t *testing.T) {
	for _, kind := range []string{"version", "unknown", "duplicate", "null", "extra-document", "legacy", "identity", "bad-point"} {
		t.Run(kind, func(t *testing.T) {
			b := newMongoIntegrationBackend(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			db, err := b.dial(ctx)
			if err != nil {
				t.Fatal(err)
			}
			state := bson.D{{Key: "version", Value: 1}, {Key: "hosts", Value: bson.M{}}}
			collection := mcoHostHistory
			switch kind {
			case "version":
				state[0].Value = 99
			case "unknown":
				state = append(state, bson.E{Key: "future", Value: true})
			case "duplicate":
				state = append(state, bson.E{Key: "version", Value: 1})
			case "legacy":
				collection = "perf_history"
			case "bad-point":
				state[1].Value = bson.M{"fixture": bson.M{"last_sample": time.Now().Unix(), "points": bson.A{bson.M{"t": 1, "future": true}}}}
			}
			var payload any = state
			if kind == "null" {
				payload = nil
			}
			doc := bson.M{"_id": "singleton", "state": payload}
			if kind == "identity" {
				doc["_id"] = "unexpected"
			}
			if _, err := db.Collection(collection).InsertOne(ctx, doc); err != nil {
				t.Fatal(err)
			}
			if kind == "extra-document" {
				if _, err := db.Collection(collection).InsertOne(ctx, bson.M{"_id": "extra", "state": state}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := db.Collection(collection).CountDocuments(ctx, bson.D{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.Load(ctx); err == nil {
				t.Fatal("unsupported history loaded")
			}
			s := NewStoreWithBackend(b, filepath.Join(t.TempDir(), "unused.json"))
			s.saveWG.Wait()
			if s.SaveChecked() == nil {
				t.Fatal("unsupported history opened gate")
			}
			if b.Save(ctx, &persistData{HostHistory: historyFixture(), Hosts: map[string]*models.Host{"must-not-write": {ID: "must-not-write"}}}) == nil {
				t.Fatal("direct save overwrote incompatible history")
			}
			after, err := db.Collection(collection).CountDocuments(ctx, bson.D{})
			if err != nil || after != before {
				t.Fatal("incompatible namespace changed")
			}
			hosts, err := db.Collection(mcoHosts).CountDocuments(ctx, bson.D{})
			if err != nil || hosts != 0 {
				t.Fatal("preflight failure wrote another collection")
			}
		})
	}
}
