package main

// Coverage for the host maintenance-mode flow — Hosts → Maintenance
// in the SPA. Drain is exercised separately because it requires a
// live VM topology + stub agent; here we focus on the flag transitions
// and authorization gates.

import (
	"net/http"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// seedHost helper places a host directly in the store so tests don't
// need the full telemetry round-trip.
func (h *integrationHarness) seedHost(id, ip string) {
	h.t.Helper()
	h.Store.ApplyTelemetry(&models.TelemetryPayload{
		HostID: id, IP: ip, AgentPort: 8081,
		Timestamp: time.Now(),
	})
}

// TestIntegration_Maintenance_EnterExit: a host.manage actor flips
// the host into maintenance and back out cleanly.
func TestIntegration_Maintenance_EnterExit(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("admin", "admin-password-12")
	h.seedHost("hv01", "10.0.0.11")

	st, body := h.do("POST", "/api/hosts/hv01/maintenance/enter",
		map[string]any{"reason": "patch kernel"})
	if st != http.StatusOK {
		t.Fatalf("enter maintenance: status=%d body=%s", st, body)
	}
	got := h.Store.GetHost("hv01")
	if got == nil || !got.MaintenanceMode {
		t.Fatalf("host not in maintenance: %+v", got)
	}
	if got.MaintenanceReason != "patch kernel" {
		t.Fatalf("reason not stored: %q", got.MaintenanceReason)
	}

	st, _ = h.do("POST", "/api/hosts/hv01/maintenance/exit", nil)
	if st != http.StatusOK {
		t.Fatalf("exit maintenance: status=%d", st)
	}
	got = h.Store.GetHost("hv01")
	if got.MaintenanceMode {
		t.Fatal("host still in maintenance after exit")
	}
}

// TestIntegration_Maintenance_RequiresHostManage: a viewer is refused.
func TestIntegration_Maintenance_RequiresHostManage(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("admin", "admin-password-12")
	h.seedHost("hv01", "10.0.0.11")
	_, _ = h.do("POST", "/api/auth/logout", nil)
	h.seedUser("viewer", "viewer-password-1", []string{"role_viewer"})
	h.mustLogin("viewer", "viewer-password-1")

	if st, _ := h.do("POST", "/api/hosts/hv01/maintenance/enter", nil); st != http.StatusForbidden {
		t.Fatalf("viewer maintenance enter: want 403, got %d", st)
	}
}

// TestIntegration_Maintenance_UnknownHost404: enter on a host that
// doesn't exist returns 404.
func TestIntegration_Maintenance_UnknownHost404(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("admin", "admin-password-12")

	if st, _ := h.do("POST", "/api/hosts/no-such-host/maintenance/enter", nil); st != http.StatusNotFound {
		t.Fatalf("enter on missing host: want 404, got %d", st)
	}
}
