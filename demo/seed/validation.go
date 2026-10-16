package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"kvm-center/internal/models"
	"kvm-center/internal/validationfixture"
)

// Validation fixtures must be isolated from real endpoints, even if a scheduler
// fires while Playwright is running. This mode is never used for production.
func validateFixtureTarget(uri, db string) error {
	if _, err := validationfixture.MongoURI(uri); err != nil {
		return err
	}
	if !strings.HasPrefix(db, "kvmc_validation_browser_") || len(db) <= len("kvmc_validation_browser_") {
		return fmt.Errorf("validation database must have a unique kvmc_validation_browser_ prefix")
	}
	return nil
}

func isolateValidationFixtures(fx *fixtures) {
	for _, h := range fx.hosts {
		h.IP = "127.0.0.1"
		h.AgentPort = 1 // no agent/host mutation can leave the local fixture
	}
	for _, p := range fx.policies {
		p.Enabled = false
	}
	for _, channel := range fx.notif.Channels {
		channel.Enabled = false
		channel.WebhookURL = ""
		channel.EmailTo = ""
	}
	fx.notif.SMTPHost = ""
	fx.features.PublicURL = "https://127.0.0.1"
	for _, m := range fx.migrations {
		// Keep history visible while preventing polling/continuation of a
		// seeded in-progress operation against a synthetic host.
		m.Status = "completed"
	}
	// A validation fixture represents read-only inventory, not libvirt
	// behavior. Do not start the synthetic agent or simulate task success.
}

// JSON fallback permits browser/API validation when MongoDB is unavailable.
// It must never be described as Mongo integration or real-host evidence.
func writeValidationJSON(path string, fx *fixtures) error {
	hosts := make(map[string]*models.Host, len(fx.hosts))
	for _, host := range fx.hosts {
		hosts[host.ID] = host
	}
	data := map[string]any{
		"hosts": hosts, "vms": fx.vms, "storage": fx.storage,
		"migrations": fx.migrations, "audit": fx.audit,
		"users": fx.users, "groups": fx.groups, "api_tokens": fx.tokens,
		"hypervisor_certs": fx.certs, "snapshot_policies": fx.policies,
		"notifications": fx.notif, "features": fx.features, "command_log": fx.cmdLog,
	}
	// Refuse to replace an existing operator file, even in validation mode.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
