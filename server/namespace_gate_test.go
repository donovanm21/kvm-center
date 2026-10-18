package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kvm-center/internal/models"
)

func TestSnapshotUnknownNamespaceRefusalPreservesOriginal(t *testing.T) {
	for _, name := range []string{"affinity_rules", "future_feature", "ALARMS"} {
		for _, value := range []string{`null`, `[]`, `{}`, `[{"id":"synthetic","enabled":true}]`} {
			raw := []byte(`{"hosts":{},"` + name + `":` + value + `}`)
			original := persistData{Hosts: map[string]*models.Host{"sentinel": {ID: "sentinel"}}}
			before, _ := json.Marshal(original)
			if err := json.Unmarshal(raw, &original); err == nil || (!strings.Contains(err.Error(), name) && !(name == "ALARMS" && strings.Contains(err.Error(), "noncanonical typed JSON field"))) {
				t.Fatal("unknown snapshot namespace accepted", name, err)
			}
			after, _ := json.Marshal(original)
			if !bytes.Equal(before, after) {
				t.Fatal("refusal mutated decode destination")
			}
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, raw, 0400); err != nil {
				t.Fatal(err)
			}
			b := NewJSONBackend(path)
			if _, err := b.Load(t.Context()); err == nil {
				t.Fatal("unknown JSON backend state accepted")
			}
			store := NewStoreWithBackend(b, path)
			store.saveWG.Wait()
			if err := store.SaveChecked(); err == nil {
				t.Fatal("refused load permitted write")
			}
			if snapshot, err := store.ExportSnapshot(); err == nil || len(snapshot) != 0 {
				t.Fatal("refused JSON load exported incomplete snapshot")
			}
			response := httptest.NewRecorder()
			handleBackup(store)(response, httptest.NewRequest("GET", "/api/admin/backup", nil))
			if response.Code != 500 || response.Header().Get("Content-Type") == "application/gzip" || bytes.HasPrefix(response.Body.Bytes(), []byte{0x1f, 0x8b}) {
				t.Fatal("refused JSON load returned partial backup")
			}
			disk, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(disk, raw) {
				t.Fatal("original rewritten", err)
			}
		}
	}
}

func TestSnapshotSupportedNamespacesAndPrivateSidecar(t *testing.T) {
	d := &persistData{Groups: map[string]*models.Group{"fixture": {ID: "fixture", Name: "known"}}, DiscordGateway: &models.DiscordGateway{Token: "synthetic-private-value"}}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back persistData
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Groups, d.Groups) || back.DiscordGateway.Token != d.DiscordGateway.Token {
		t.Fatal("supported snapshot changed")
	}
}
