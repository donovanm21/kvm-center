package main

// VM edit + CD/DVD media integration flows (v5.27). Exercises the
// server-side permission gate; agent-side virsh execution is covered
// by agent unit tests (the integration suite doesn't spin up libvirt).

import (
	"net/http"
	"testing"
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
