package main

// Cross-handler integration flows (v5.27). Each test drives a real
// operator scenario end-to-end through the harness.

import (
	"net/http"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// TestIntegration_APIToken_MintAndUse: admin mints a self-service
// token, then uses it with Bearer auth to hit an admin endpoint
// (proving the token inherits owner permissions).
func TestIntegration_APIToken_MintAndUse(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")

	var mint struct {
		Token string `json:"token"`
	}
	st := h.doJSON("POST", "/api/tokens/me", map[string]any{
		"name":       "integration-test",
		"expires_at": "",
	}, &mint)
	if st != http.StatusOK {
		t.Fatalf("mint token: status=%d", st)
	}
	if mint.Token == "" {
		t.Fatal("mint token: empty token in response")
	}

	// Use the Bearer token on a fresh client (no cookie) against an
	// admin-gated endpoint. The token inherits Alice's admin scope.
	st, body := h.doWithToken("GET", "/api/users", mint.Token, nil)
	if st != http.StatusOK {
		t.Fatalf("/api/users with bearer: status=%d body=%s", st, body)
	}
}

// TestIntegration_APIToken_Revoke: revoked tokens stop working
// immediately, even if their TTL hasn't expired.
func TestIntegration_APIToken_Revoke(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")

	var mint struct {
		Token  string `json:"token"`
		Record struct {
			ID string `json:"id"`
		} `json:"record"`
	}
	if st := h.doJSON("POST", "/api/tokens/me", map[string]any{"name": "to-be-revoked"}, &mint); st != http.StatusOK {
		t.Fatalf("mint: status=%d", st)
	}
	// Sanity: works before revoke.
	if st, _ := h.doWithToken("GET", "/api/auth/me", mint.Token, nil); st != http.StatusOK {
		t.Fatalf("pre-revoke /me: status=%d", st)
	}
	// Revoke via self-service endpoint.
	if st, _ := h.do("DELETE", "/api/tokens/me/"+mint.Record.ID, nil); st != http.StatusOK {
		t.Fatalf("revoke: status=%d", st)
	}
	// Post-revoke the same token must be refused.
	if st, _ := h.doWithToken("GET", "/api/auth/me", mint.Token, nil); st != http.StatusUnauthorized {
		t.Fatalf("post-revoke /me: want 401, got %d", st)
	}
}

// TestIntegration_FeatureFlag_DisableSnapshots: /api/settings/update
// preserves SetupComplete + PublicURL while flipping Disable*.
// Also verifies that after flipping DisableSnapshots the feature
// flag is reflected in GetFeatures.
func TestIntegration_FeatureFlag_DisableSnapshots(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")

	// Seed a value the update shouldn't clobber.
	h.Store.SetFeatures(models.Features{
		SetupComplete: true,
		PublicURL:     "https://test.example.com:8190",
	})

	st, body := h.do("PUT", "/api/settings/update", map[string]bool{
		"disable_snapshots": true,
	})
	if st != http.StatusOK {
		t.Fatalf("update features: status=%d body=%s", st, body)
	}

	got := h.Store.GetFeatures()
	if !got.DisableSnapshots {
		t.Fatal("DisableSnapshots didn't persist")
	}
	if !got.SetupComplete {
		t.Fatal("SetupComplete clobbered — regression of v5.20.3 fix")
	}
	if got.PublicURL != "https://test.example.com:8190" {
		t.Fatalf("PublicURL clobbered: got %q — regression of v5.23.0 fix", got.PublicURL)
	}
}

// TestIntegration_HostDelete: the v5.26.0 DELETE /api/hosts/{id} flow.
// Seeds a host via ApplyTelemetry, deletes it, asserts the host + VM
// records are gone and the endpoint refuses the repeat delete.
func TestIntegration_HostDelete(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")

	h.Store.ApplyTelemetry(&models.TelemetryPayload{
		HostID:    "hvtest01",
		IP:        "10.0.0.11",
		AgentPort: 8081,
		VMs: []models.VM{
			{Name: "vm-a", UUID: "uuid-a", HostID: "hvtest01", State: models.VMRunning},
			{Name: "vm-b", UUID: "uuid-b", HostID: "hvtest01", State: models.VMStopped},
		},
		Timestamp: time.Now(),
	})

	// Pre-condition: the host and its VMs appear in the inventory.
	if host := h.Store.GetHost("hvtest01"); host == nil {
		t.Fatal("seeded host not found")
	}
	if vms := h.Store.GetVMs("hvtest01"); len(vms) != 2 {
		t.Fatalf("seeded VMs: want 2, got %d", len(vms))
	}

	st, body := h.do("DELETE", "/api/hosts/hvtest01", nil)
	if st != http.StatusOK {
		t.Fatalf("delete host: status=%d body=%s", st, body)
	}
	if host := h.Store.GetHost("hvtest01"); host != nil {
		t.Fatal("host still present after delete")
	}
	if vms := h.Store.GetVMs("hvtest01"); len(vms) != 0 {
		t.Fatalf("VM rows survived delete: got %d", len(vms))
	}

	// Repeat delete is a 404 — helps detect double-delete regressions
	// that could corrupt audit logs.
	if st, _ := h.do("DELETE", "/api/hosts/hvtest01", nil); st != http.StatusNotFound {
		t.Fatalf("repeat delete: want 404, got %d", st)
	}
}

// TestIntegration_HostDelete_RefusesDuringMigration: DELETE returns
// 409 when a running migration references the host. Safety check
// for the v5.26.0 feature.
func TestIntegration_HostDelete_RefusesDuringMigration(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")

	h.Store.ApplyTelemetry(&models.TelemetryPayload{
		HostID: "hvtest01", IP: "10.0.0.11", AgentPort: 8081,
		Timestamp: time.Now(),
	})
	h.Store.AddMigration(&models.MigrationRecord{
		ID:         "mig-1",
		VMName:     "test-vm",
		SourceHost: "hvtest01",
		DestHost:   "hvtest02",
		Status:     "running",
		StartedAt:  time.Now().Unix(),
	})

	st, _ := h.do("DELETE", "/api/hosts/hvtest01", nil)
	if st != http.StatusConflict {
		t.Fatalf("delete during migration: want 409, got %d", st)
	}
	// And the host is still there.
	if host := h.Store.GetHost("hvtest01"); host == nil {
		t.Fatal("host wrongly removed despite 409")
	}
}

// TestIntegration_Setup_PublicURL: admin writes Features.PublicURL
// via /api/settings/public-url; /api/setup/state reflects it.
func TestIntegration_Setup_PublicURL(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")

	st, body := h.do("POST", "/api/settings/public-url", map[string]string{
		"public_url": "https://kvm.example.com:8190",
	})
	if st != http.StatusOK {
		t.Fatalf("set public URL: status=%d body=%s", st, body)
	}

	var state map[string]any
	if st := h.doJSON("GET", "/api/setup/state", nil, &state); st != http.StatusOK {
		t.Fatalf("setup state: status=%d", st)
	}
	if state["public_url"] != "https://kvm.example.com:8190" {
		t.Fatalf("setup state public_url: got %v", state["public_url"])
	}

	// Rejects an unscheme'd value.
	if st, _ := h.do("POST", "/api/settings/public-url", map[string]string{
		"public_url": "kvm.example.com",
	}); st != http.StatusBadRequest {
		t.Fatalf("unscheme'd URL: want 400, got %d", st)
	}
}

// TestIntegration_Health is the smoke test CI hits at the end of
// every deploy. Keeping it on the integration suite means a future
// auth-middleware change can't accidentally lock out the healthcheck.
func TestIntegration_Health(t *testing.T) {
	h := newIntegrationHarness(t)
	// No auth on purpose.
	c := &http.Client{}
	resp, err := c.Get(h.Server.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health: want 200, got %d", resp.StatusCode)
	}
}
