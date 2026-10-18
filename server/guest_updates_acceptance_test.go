//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"encoding/json"
	"kvm-center/internal/models"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryAcceptanceGuestUpdateJSON(t *testing.T)  { guestUpdateAcceptance(t, "json") }
func TestHistoryAcceptanceGuestUpdateMongo(t *testing.T) { guestUpdateAcceptance(t, "mongo") }
func guestUpdateAcceptance(t *testing.T, mode string) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("current artifact required")
	}
	root := t.TempDir()
	var b Backend = NewJSONBackend(filepath.Join(root, "state.json"))
	if mode == "mongo" {
		b = newMongoIntegrationBackend(t)
		requireInactiveGuestTTL(t, b.(*MongoBackend))
	}
	s, _ := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	s.mu.Lock()
	s.roles["fixture-role"].Permissions = append(s.roles["fixture-role"].Permissions, "vm.edit")
	s.vms["synthetic"] = []*models.VM{{UUID: updateVM1, HostID: "synthetic"}, {UUID: updateVM2, HostID: "synthetic"}}
	s.hosts["synthetic"] = &models.Host{ID: "synthetic", Status: "offline"}
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("guest update acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage", "vm.edit"})
	if err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err = s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	p := startAcceptance(t, binary, root, b, token, false)
	queue := []byte(`{"request_id":"synthetic-update","uuids":["` + updateVM1 + `"]}`)
	p.request(t, "POST", "/api/kvm-tools/update", []byte(`{"uuids":[]}`), 400)
	p.request(t, "POST", "/api/kvm-tools/update", queue, 200)
	staleArchive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	report := []byte(`{"vm_uuid":"` + updateVM1 + `","hostname":"synthetic","update_os":"linux","arch":"amd64"}`)
	if bytes.Contains(p.request(t, "POST", "/api/guest-telemetry", []byte(`{"vm_uuid":"`+updateVM1+`"}`), 200), []byte("update_url")) {
		t.Fatal("unknown platform offered")
	}
	reply := p.request(t, "POST", "/api/guest-telemetry", report, 200)
	var offer map[string]any
	if json.Unmarshal(reply, &offer) != nil || offer["update_request_id"] != "synthetic-update" || offer["update_url"] == nil {
		t.Fatal("missing offer identity")
	}
	verify := func(status string) {
		t.Helper()
		if bytes.Contains(p.request(t, "POST", "/api/guest-telemetry", report, 200), []byte("update_url")) {
			t.Fatal("repeat offer")
		}
		for _, raw := range [][]byte{p.request(t, "POST", "/api/kvm-tools/update", queue, 200), p.request(t, "GET", "/api/kvm-tools/update?request_id=synthetic-update", nil, 200)} {
			if !bytes.Contains(raw, []byte(status)) || bytes.Contains(raw, []byte("update_url")) {
				t.Fatal("replay status mismatch")
			}
		}
	}
	verify("offered_unconfirmed")
	p.request(t, "POST", "/api/kvm-tools/update", []byte(`{"request_id":"overlap","uuids":["`+updateVM1+`"]}`), 409)
	p.request(t, "POST", "/api/kvm-tools/update", []byte(`{"request_id":"other","uuids":["`+updateVM2+`"]}`), 200)
	p.stop(t, false)
	p = startAcceptance(t, binary, root, b, token, false)
	verify("offered_unconfirmed")
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	for _, tc := range []struct {
		archive []byte
		status  string
	}{{archive, "offered_unconfirmed"}, {staleArchive, "recovery_fenced"}} {
		restored := t.TempDir()
		snapshot := unpackAcceptance(t, tc.archive, restored, 0)
		if snapshot.GuestUpdates == nil {
			t.Fatal("backup lost ledger")
		}
		if mode == "mongo" {
			b = newMongoIntegrationBackend(t)
			requireInactiveGuestTTL(t, b.(*MongoBackend))
		} else {
			b = NewJSONBackend(filepath.Join(restored, "state.json"))
		}
		p = startAcceptance(t, binary, restored, b, token, false)
		verify(tc.status)
		p.stop(t, false)
		p = startAcceptance(t, binary, restored, b, token, false)
		verify(tc.status)
		p.stop(t, false)
	}
	emitAcceptance(t, "guest_update_offer_lifecycle", map[string]any{"backend": mode, "ledger_schema": 1, "request_binding_and_overlap_refusal": true, "one_durable_offer": true, "replay_never_reoffers": true, "restart_backup_restore_restart": true, "stale_queued_restore_fenced": true, "guest_effects_verified": false, "binary_downloaded_or_executed": false, "full_legacy_compatible": false})
}
