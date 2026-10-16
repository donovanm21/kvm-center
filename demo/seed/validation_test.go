package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidationFixtureRejectsSharedTargets(t *testing.T) {
	for _, tc := range [][2]string{
		{"mongodb://production:27017", "kvmc_validation_browser_123"},
		{"mongodb://user:pass@localhost", "kvmc_validation_browser_123"},
		{"mongodb://localhost", "production"},
		{"mongodb://localhost", "kvmc_validation_browser_"},
	} {
		if validateFixtureTarget(tc[0], tc[1]) == nil {
			t.Fatalf("accepted unsafe target %v", tc)
		}
	}
	if err := validateFixtureTarget("mongodb://127.0.0.1:27017", "kvmc_validation_browser_123"); err != nil {
		t.Fatal(err)
	}
}

func TestValidationFixtureDisablesExternalEffects(t *testing.T) {
	fx := buildFixtures()
	isolateValidationFixtures(fx)
	for _, h := range fx.hosts {
		if h.IP != "127.0.0.1" || h.AgentPort != 1 {
			t.Fatal("fixture contains external host endpoint")
		}
	}
	for _, p := range fx.policies {
		if p.Enabled {
			t.Fatal("snapshot scheduler remains enabled")
		}
	}
	for _, c := range fx.notif.Channels {
		if c.Enabled || c.WebhookURL != "" || c.EmailTo != "" {
			t.Fatal("external notification remains configured")
		}
	}
	for _, m := range fx.migrations {
		if m.Status != "completed" {
			t.Fatal("migration remains active")
		}
	}
}

func TestValidationJSONIsIsolatedAndNeverOverwrites(t *testing.T) {
	fx := buildFixtures()
	isolateValidationFixtures(fx)
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := writeValidationJSON(path, fx); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"hosts", "vms", "users", "features", "notifications"} {
		if len(decoded[field]) < 3 {
			t.Fatalf("missing JSON fixture field %s", field)
		}
	}
	if err := writeValidationJSON(path, fx); err == nil {
		t.Fatal("overwrote existing fixture")
	}
}
