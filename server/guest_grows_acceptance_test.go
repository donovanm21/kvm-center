//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"kvm-center/internal/growguest"
	"kvm-center/internal/growprotocol"
	"kvm-center/internal/models"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryAcceptanceGuestGrowJSON(t *testing.T)  { guestGrowAcceptance(t, "json") }
func TestHistoryAcceptanceGuestGrowMongo(t *testing.T) { guestGrowAcceptance(t, "mongo") }
func guestGrowAcceptance(t *testing.T, mode string) {
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
	s.vms["synthetic"] = []*models.VM{{UUID: updateVM1, HostID: "synthetic"}}
	s.hosts["synthetic"] = &models.Host{ID: "synthetic", Status: "offline"}
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("guest grow acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage", "vm.edit"})
	if err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err = s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	gpath := filepath.Join(t.TempDir(), "guest.sqlite")
	g, err := growguest.OpenGuest(gpath, updateVM1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { g.Close() }()
	p := startAcceptance(t, binary, root, b, token, false)
	exchange := func() *growprotocol.Request {
		t.Helper()
		report, e := g.Report(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		body, _ := json.Marshal(map[string]any{"vm_uuid": updateVM1, "grow_v1": report})
		raw := p.request(t, "POST", "/api/guest-telemetry", body, 200)
		var reply struct {
			Request *growprotocol.Request `json:"grow_request_v1"`
		}
		if json.Unmarshal(raw, &reply) != nil {
			t.Fatal("bad offer")
		}
		return reply.Request
	}
	if exchange() != nil {
		t.Fatal("unsolicited request")
	}
	req := growTestRequest(g.Session())
	raw, _ := json.Marshal(req)
	p.request(t, "POST", "/api/guest-grow", raw, 202)
	stale := p.request(t, "GET", "/api/admin/backup", nil, 200)
	offer := exchange()
	if offer == nil || *offer != req {
		t.Fatal("missing correlated offer")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, root, b, token, false)
	if retry := exchange(); retry == nil || *retry != req {
		t.Fatal("lost response changed offer")
	}
	result, err := g.Run(context.Background(), *offer, nil)
	if err != nil || result.Status != "refused_native_unqualified" {
		t.Fatal("production execution gate", err)
	}
	if again, e := g.Run(context.Background(), *offer, nil); e != nil || again != result {
		t.Fatal("guest dedup")
	}
	g.Close()
	g, err = growguest.OpenGuest(gpath, updateVM1)
	if err != nil {
		t.Fatal(err)
	}
	if exchange() != nil || exchange() != nil {
		t.Fatal("completed request reoffered")
	}
	verify := func(phase string) {
		t.Helper()
		view := p.request(t, "GET", "/api/guest-grow?uuid="+updateVM1, nil, 200)
		if !bytes.Contains(view, []byte(phase)) || !bytes.Contains(view, []byte(`"native_verified":false`)) {
			t.Fatal("wrong retained status")
		}
	}
	verify("reported")
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	for _, tc := range []struct {
		archive []byte
		phase   string
	}{{stale, "recovery_fenced"}, {archive, "reported"}} {
		restored := t.TempDir()
		snapshot := unpackAcceptance(t, tc.archive, restored, 0)
		if snapshot.GuestGrows == nil || len(snapshot.GuestGrows.Records) != 1 {
			t.Fatal("backup lost intent")
		}
		if mode == "mongo" {
			b = newMongoIntegrationBackend(t)
			requireInactiveGuestTTL(t, b.(*MongoBackend))
		} else {
			b = NewJSONBackend(filepath.Join(restored, "state.json"))
		}
		for i := 0; i < 2; i++ {
			p = startAcceptance(t, binary, restored, b, token, false)
			verify(tc.phase)
			if exchange() != nil {
				t.Fatal("restore offered stale request")
			}
			p.stop(t, false)
		}
	}
	emitAcceptance(t, "guest_grow_protocol_lifecycle", map[string]any{"backend": mode, "ledger_schema": 1, "correlated_retry": true, "guest_durable_deduplication": true, "guest_restart_retains_outcomes": true, "restart_backup_restore_restart": true, "stale_queued_restore_fenced": true, "native_adapter_absent": true, "filesystem_effects_verified": false, "full_legacy_compatible": false})
}
