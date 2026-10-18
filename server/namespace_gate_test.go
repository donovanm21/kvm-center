package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kvm-center/internal/models"
)

func TestSnapshotUnknownNamespaceRefusalPreservesOriginal(t *testing.T) {
	for _, name := range []string{"alarms", "affinity_rules", "future_feature", "ALARMS"} {
		for _, value := range []string{`null`, `[]`, `{}`, `[{"id":"synthetic","enabled":true}]`} {
			raw := []byte(`{"hosts":{},"` + name + `":` + value + `}`)
			original := persistData{Hosts: map[string]*models.Host{"sentinel": {ID: "sentinel"}}}
			before, _ := json.Marshal(original)
			if err := json.Unmarshal(raw, &original); err == nil || !strings.Contains(err.Error(), name) {
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
