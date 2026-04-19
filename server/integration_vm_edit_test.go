package main

// VM edit + CD/DVD media integration flows (v5.27). Exercises the
// server-side permission gate; agent-side virsh execution is covered
// by agent unit tests (the integration suite doesn't spin up libvirt).

import (
	"net/http"
	"strings"
	"testing"

	"kvm-center/internal/models"
)

// TestIntegration_Command_EditVM_RequiresVMEdit: a Viewer hitting
// EDIT_VM is refused by the permission gate before the agent is
// contacted. 403 is the success signal — 200 would mean the bundle
// was silently widened past vm.edit.
func TestIntegration_Command_EditVM_RequiresVMEdit(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")
	_, _ = h.do("POST", "/api/auth/logout", nil)
	h.seedUser("bob", "bob-password-12", []string{"role_viewer"})
	h.mustLogin("bob", "bob-password-12")

	st, _ := h.do("POST", "/api/command", map[string]any{
		"host_id": "hv01",
		"vm_uuid": "some-uuid",
		"action":  "EDIT_VM",
		"params":  map[string]string{"vcpus": "4"},
	})
	if st != http.StatusForbidden {
		t.Fatalf("viewer EDIT_VM: want 403, got %d", st)
	}
}

// TestIntegration_Command_ChangeMedia_RequiresVMEdit mirrors the
// EDIT_VM test for the ISO swap command.
func TestIntegration_Command_ChangeMedia_RequiresVMEdit(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")
	_, _ = h.do("POST", "/api/auth/logout", nil)
	h.seedUser("bob", "bob-password-12", []string{"role_viewer"})
	h.mustLogin("bob", "bob-password-12")

	st, _ := h.do("POST", "/api/command", map[string]any{
		"host_id": "hv01",
		"vm_uuid": "some-uuid",
		"action":  "CHANGE_MEDIA",
		"params":  map[string]string{"media_action": "eject", "target": "hda"},
	})
	if st != http.StatusForbidden {
		t.Fatalf("viewer CHANGE_MEDIA: want 403, got %d", st)
	}
}

// TestIntegration_Command_OperatorCanEditVM: the Operator role now
// bundles vm.edit (v5.25.0). An operator trying EDIT_VM gets past the
// permission gate; the 404-on-missing-host that follows is the
// expected downstream signal (no host configured in this test).
func TestIntegration_Command_OperatorCanEditVM(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")
	_, _ = h.do("POST", "/api/auth/logout", nil)
	h.seedUser("opo", "opo-password-12", []string{"role_operator"})
	h.mustLogin("opo", "opo-password-12")

	st, _ := h.do("POST", "/api/command", map[string]any{
		"host_id": "no-such-host",
		"vm_uuid": "u",
		"action":  "EDIT_VM",
		"params":  map[string]string{"vcpus": "4"},
	})
	// 404 from the host-not-found branch, NOT 403 — confirms the
	// permission gate passed for an Operator.
	if st == http.StatusForbidden {
		t.Fatalf("operator EDIT_VM: got 403 (vm.edit missing from role?)")
	}
}

// TestIntegration_Command_EditVM_AgentResponseShape: admin issues
// EDIT_VM through /api/command; the stub agent returns a canned
// response with requires_reboot:true so the SPA's shutdown-prompt
// path is exercised end-to-end.
func TestIntegration_Command_EditVM_AgentResponseShape(t *testing.T) {
	h := newIntegrationHarness(t)
	h.registerFirstAdmin("alice", "alice-password-12")
	sa := newStubAgent(t)
	sa.nextBody = []byte(`{"success":true,"message":"EDIT_VM: vcpus=8 (config saved — reboot required to apply)","requires_reboot":true}`)

	// Register a host pointing at the stub agent.
	u := sa.Server.URL // http://127.0.0.1:NNNN
	ip := "127.0.0.1"
	port := 0
	for i := 0; i < len(u); i++ {
		if u[i] == ':' && i > 5 {
			for j := i + 1; j < len(u); j++ {
				if u[j] < '0' || u[j] > '9' {
					break
				}
				port = port*10 + int(u[j]-'0')
			}
			break
		}
	}
	h.Store.ApplyTelemetry(&models.TelemetryPayload{
		HostID:    "hv-edit",
		IP:        ip,
		AgentPort: port,
		VMs:       []models.VM{{Name: "test-vm", UUID: "uuid-1", HostID: "hv-edit", State: models.VMRunning}},
	})

	st, body := h.do("POST", "/api/command", map[string]any{
		"host_id": "hv-edit",
		"vm_uuid": "uuid-1",
		"vm_name": "test-vm",
		"action":  "EDIT_VM",
		"params":  map[string]string{"vcpus": "8"},
	})
	if st != http.StatusOK {
		t.Fatalf("EDIT_VM: status=%d body=%s", st, body)
	}
	// Server relays the agent's response verbatim.
	if !strings.Contains(string(body), `"requires_reboot":true`) {
		t.Fatalf("response missing requires_reboot flag: %s", body)
	}
	// Stub agent received exactly one EDIT_VM command.
	cmds := sa.Commands()
	if len(cmds) != 1 || cmds[0].Action != "EDIT_VM" || cmds[0].Params["vcpus"] != "8" {
		t.Fatalf("stub agent commands: %+v", cmds)
	}
}
