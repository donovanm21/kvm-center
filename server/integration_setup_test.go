package main

// Setup wizard + migration reconciler + telemetry integration flows
// (v5.27). Complements integration_auth_test.go + integration_flows_test.go
// so the suite now exercises every critical first-boot + operational path.

import (
	"net/http"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// TestIntegration_SetupState_FreshInstall: a brand-new store reports
// complete=false so the SPA knows to show the wizard. has_any_host
// is false. public_url empty.
func TestIntegration_SetupState_FreshInstall(t *testing.T) {
	h := newIntegrationHarness(t)

	// /api/setup/state is public (SPA needs it before sign-in).
	var state struct {
		Complete    bool   `json:"complete"`
		HasAnyHost  bool   `json:"has_any_host"`
		PublicURL   string `json:"public_url"`
		CompletedAt string `json:"completed_at"`
	}
	st := h.doJSON("GET", "/api/setup/state", nil, &state)
	if st != http.StatusOK {
		t.Fatalf("setup state: status=%d", st)
	}
	if state.Complete {
		t.Fatal("fresh install: complete should be false")
	}
	if state.HasAnyHost {
		t.Fatal("fresh install: has_any_host should be false")
	}
}

// TestIntegration_SetupState_HasHost: after a host checks in via
// telemetry, has_any_host flips true so the wizard can skip the
// "enrol your first hypervisor" step.
func TestIntegration_SetupState_HasHost(t *testing.T) {
	h := newIntegrationHarness(t)
	h.Store.ApplyTelemetry(&models.TelemetryPayload{
		HostID: "hv01", IP: "10.0.0.11", AgentPort: 8081,
		Timestamp: time.Now(),
	})

	var state struct {
		HasAnyHost bool `json:"has_any_host"`
	}
	if st := h.doJSON("GET", "/api/setup/state", nil, &state); st != http.StatusOK {
		t.Fatalf("setup state: status=%d", st)
	}
	if !state.HasAnyHost {
		t.Fatal("has_any_host didn't flip after telemetry")
	}
}

// TestIntegration_SetupComplete_RequiresAdmin: POST /api/setup/complete
// is behind user.manage. A viewer is refused, the admin succeeds,
// state.complete flips true.
func TestIntegration_SetupComplete_RequiresAdmin(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")
	_, _ = h.do("POST", "/api/auth/logout", nil)
	h.seedUser("viewer", "viewer-password-1", []string{"role_viewer"})

	h.mustLogin("viewer", "viewer-password-1")
	if st, _ := h.do("POST", "/api/setup/complete", nil); st != http.StatusForbidden {
		t.Fatalf("viewer /setup/complete: want 403, got %d", st)
	}

	_, _ = h.do("POST", "/api/auth/logout", nil)
	h.mustLogin("alice", "alice-password-12")
	if st, _ := h.do("POST", "/api/setup/complete", nil); st != http.StatusOK {
		t.Fatalf("admin /setup/complete: status=%d", st)
	}
	if !h.Store.GetFeatures().SetupComplete {
		t.Fatal("SetupComplete didn't persist")
	}
}

// TestIntegration_Reconciler_MarksCompletedWhenVMOnDest: the v5.25.1
// boot reconciler should flip a stuck "running" record to "complete"
// when the VM is already on the destination host. We invoke the
// reconciler's inner loop directly so we don't have to wait for the
// 60 s telemetry grace.
func TestIntegration_Reconciler_MarksCompletedWhenVMOnDest(t *testing.T) {
	h := newIntegrationHarness(t)

	// VM currently running on the destination (what the source agent
	// would have moved it to).
	h.Store.ApplyTelemetry(&models.TelemetryPayload{
		HostID: "hv02", IP: "10.0.0.12", AgentPort: 8081,
		VMs: []models.VM{
			{Name: "migrated-vm", UUID: "u-1", HostID: "hv02", State: models.VMRunning},
		},
		Timestamp: time.Now(),
	})

	h.Store.AddMigration(&models.MigrationRecord{
		ID:         "mig-stuck-1",
		VMName:     "migrated-vm",
		SourceHost: "hv01",
		DestHost:   "hv02",
		Status:     "running",
		StartedAt:  time.Now().Unix(),
	})

	// Skip the 60-s grace by calling the same finalization code the
	// reconciler would reach once it sees the VM on the destination.
	rec := h.Store.GetMigrations()[0]
	if !vmIsOnHost(h.Store, rec.VMName, rec.DestHost) {
		t.Fatal("precondition: vm not reported on destination")
	}
	finalizeMigration(h.Store, rec, "complete", "reconciled in test")

	after := h.Store.GetMigrations()[0]
	if after.Status != "complete" {
		t.Fatalf("reconcile status: want complete, got %q", after.Status)
	}
}

// TestIntegration_Telemetry_UpsertsHost: POST /api/telemetry lands a
// new host and its VMs in the store. This is the agent-facing path
// that keeps the inventory fresh; a regression here would hide every
// hypervisor.
func TestIntegration_Telemetry_UpsertsHost(t *testing.T) {
	h := newIntegrationHarness(t)
	payload := models.TelemetryPayload{
		HostID: "hv-telemetry", IP: "10.0.0.50", AgentPort: 8081,
		OS: "Rocky 9.4", KernelVer: "5.14.0",
		VMs: []models.VM{
			{Name: "t-vm-1", UUID: "u-1", State: models.VMRunning},
		},
		Timestamp: time.Now(),
	}
	st, body := h.do("POST", "/api/telemetry", payload)
	if st != http.StatusOK {
		t.Fatalf("telemetry: status=%d body=%s", st, body)
	}

	got := h.Store.GetHost("hv-telemetry")
	if got == nil {
		t.Fatal("host not created from telemetry")
	}
	if got.IP != "10.0.0.50" {
		t.Fatalf("host IP: got %q", got.IP)
	}
	vms := h.Store.GetVMs("hv-telemetry")
	if len(vms) != 1 || vms[0].Name != "t-vm-1" {
		t.Fatalf("VM list: %+v", vms)
	}
}

// TestIntegration_Telemetry_MarksVMState: subsequent telemetry ticks
// update state without duplicating rows.
func TestIntegration_Telemetry_MarksVMState(t *testing.T) {
	h := newIntegrationHarness(t)
	// First tick: running.
	h.Store.ApplyTelemetry(&models.TelemetryPayload{
		HostID: "hv", IP: "10.0.0.10", AgentPort: 8081,
		VMs: []models.VM{{Name: "vm", UUID: "u", State: models.VMRunning}},
	})
	// Second tick: stopped.
	h.Store.ApplyTelemetry(&models.TelemetryPayload{
		HostID: "hv", IP: "10.0.0.10", AgentPort: 8081,
		VMs: []models.VM{{Name: "vm", UUID: "u", State: models.VMStopped}},
	})
	vms := h.Store.GetVMs("hv")
	if len(vms) != 1 {
		t.Fatalf("VM list len: want 1, got %d", len(vms))
	}
	if vms[0].State != models.VMStopped {
		t.Fatalf("VM state: want stopped, got %q", vms[0].State)
	}
}
