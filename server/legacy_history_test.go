package main

import (
	"bytes"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/offlinestaging"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
)

func legacyStateFixture(t *testing.T) *legacyHistoryState {
	t.Helper()
	f := legacyfixture.GenerateHistoryReader()
	id := bson.Raw(f.Documents["perf_history"]).Lookup("_id").StringValue()
	s := &legacyHistoryState{Version: 1, SourceContract: offlinestaging.LegacyHistorySource, Records: map[string][]byte{id: f.Documents["perf_history"]}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestActiveLegacyHistoryJSONAndAPI(t *testing.T) {
	state := legacyStateFixture(t)
	id := ""
	for key := range state.Records {
		id = key
	}
	b := NewJSONBackend(filepath.Join(t.TempDir(), "state.json"))
	d := &persistData{LegacyHistory: state}
	if err := b.Save(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	back, err := b.Load(t.Context())
	if err != nil || !reflect.DeepEqual(back.LegacyHistory, state) {
		t.Fatal("JSON roundtrip", err)
	}
	if err := verifyBackendRoundTrip(t.Context(), d, b); err != nil {
		t.Fatal(err)
	}
	s := &Store{legacyHistory: back.LegacyHistory}
	for _, tc := range []struct {
		query string
		code  int
	}{{"?source=legacy", 200}, {"", 404}, {"?source=legacy&range=1h", 400}, {"?source=legacy&source=current", 400}, {"?source=future", 400}} {
		w := httptest.NewRecorder()
		handleHostHistory(s)(w, httptest.NewRequest("GET", "/api/hosts/"+id+"/history"+tc.query, nil))
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.query, w.Code, w.Body)
		}
		if tc.code == 200 {
			if !bytes.Contains(w.Body.Bytes(), []byte(`"read_only":true`)) || !bytes.Contains(w.Body.Bytes(), []byte(`"name":"1d"`)) {
				t.Fatal("missing retained tiers", w.Body)
			}
		}
	}
	w := httptest.NewRecorder()
	requirePerm(s, "host.view", handleHostHistory(s)).ServeHTTP(w, httptest.NewRequest("GET", "/api/hosts/"+id+"/history?source=legacy", nil))
	if w.Code < 400 {
		t.Fatal("permission bypass")
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var decoded persistData
	if json.Unmarshal(raw, &decoded) != nil || !reflect.DeepEqual(decoded.LegacyHistory, state) {
		t.Fatal("snapshot codec")
	}
	features, err := snapshotFeatureSchemas(raw)
	if err != nil || features["legacy_history_state"] != 1 {
		t.Fatal("backup manifest feature missing")
	}
}
