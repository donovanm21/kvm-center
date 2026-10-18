//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"kvm-center/internal/affinitypolicy"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHistoryAcceptanceAffinityJSON(t *testing.T)       { affinityAcceptance(t, "json", false) }
func TestHistoryAcceptanceAffinityMongo(t *testing.T)      { affinityAcceptance(t, "mongo", false) }
func TestHistoryAcceptanceAffinityDrainJSON(t *testing.T)  { affinityAcceptance(t, "json", true) }
func TestHistoryAcceptanceAffinityDrainMongo(t *testing.T) { affinityAcceptance(t, "mongo", true) }
func affinityAcceptance(t *testing.T, mode string, drain bool) {
	binary, offline := os.Getenv("KVMC_HISTORY_BINARY"), os.Getenv("KVMC_OFFLINE_BINARY")
	if binary == "" || offline == "" {
		t.Fatal("built artifacts required")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	fixture := legacyfixture.GenerateAffinity()
	if err := legacyfixture.Write(source, fixture); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"snapshot.json", "affinity_rules.bson", "affinity_rules.indexes.bson"} {
		if err := os.Chmod(filepath.Join(source, name), 0400); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, offline, args...).Output()
		if err != nil {
			t.Fatal("offline artifact", err)
		}
		return out
	}
	stage := filepath.Join(root, "stage")
	run("-mode", "convert", "-source", source, "-bundle", stage)
	export := run("-mode", "legacy-affinity-state", "-bundle", stage)
	var state affinitypolicy.State
	if err := json.Unmarshal(export, &state); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/command" {
			var c models.CommandRequest
			if json.NewDecoder(r.Body).Decode(&c) != nil || c.VMUUID != legacyfixture.VMID || c.Params["destination"] != "192.0.2.3" {
				t.Error("unexpected synthetic dispatch")
			}
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"active":false,"message":"synthetic intercepted completion"}`))
	}))
	defer agent.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(agent.URL, "http://"))
	agentPort, _ := strconv.Atoi(port)
	s, _ := acceptanceSeed(t, NewJSONBackend(filepath.Join(root, "state.json")), filepath.Join(root, "state.json"), false)
	s.mu.Lock()
	s.affinity = state.Clone()
	now := time.Now()
	s.hosts["source"] = &models.Host{ID: "source", IP: "127.0.0.1", AgentPort: agentPort, Status: models.HostOnline, LastSeen: now}
	s.hosts["peer"] = &models.Host{ID: "peer", IP: "192.0.2.2", Status: models.HostOnline, LastSeen: now, MemUsage: 1}
	s.hosts["target"] = &models.Host{ID: "target", IP: "192.0.2.3", Status: models.HostOnline, LastSeen: now, MemUsage: 50}
	s.vms["source"] = []*models.VM{{UUID: legacyfixture.VMID, Name: "one", HostID: "source", State: models.VMRunning}, {UUID: legacyfixture.AffinityVM3, Name: "three", HostID: "source", State: models.VMRunning}}
	s.vms["peer"] = []*models.VM{{UUID: legacyfixture.AffinityVM2, Name: "two", HostID: "peer", State: models.VMRunning}}
	s.roles["fixture-role"].Permissions = append(s.roles["fixture-role"].Permissions, "vm.migrate", "host.manage")
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("affinity acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage", "vm.migrate", "host.manage"})
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the process-owned pre-dispatch abort before starting the native
	// application. Recovery must retain its replay tombstone and admit a new ID.
	q := affinityReservationFor("native-affinity-aborted")
	_, lease, err := s.reserveAffinity(q)
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.abortPrepared(); err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err = s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	backend := func(root string) Backend {
		if mode == "mongo" {
			b := newMongoIntegrationBackend(t)
			requireInactiveGuestTTL(t, b)
			return b
		}
		return NewJSONBackend(filepath.Join(root, "state.json"))
	}
	b := backend(root)
	p := startAcceptance(t, binary, root, b, token, false)
	request := func(dest string) []byte {
		raw, _ := json.Marshal(models.APICommandRequest{RequestID: "native-affinity-request", Action: "MIGRATE", HostID: "source", VMUUID: legacyfixture.VMID, VMName: "one", Params: map[string]string{"destination": dest}})
		return raw
	}
	oldRequest, _ := json.Marshal(models.APICommandRequest{RequestID: "native-affinity-aborted", Action: "MIGRATE", HostID: "source", VMUUID: legacyfixture.VMID, VMName: "one", Params: map[string]string{"destination": "192.0.2.3"}})
	p.request(t, "POST", "/api/command", oldRequest, 409)
	if calls.Load() != 0 {
		t.Fatal("aborted request replayed after native boot")
	}
	refused := p.request(t, "POST", "/api/command", request("192.0.2.2"), 409)
	if !bytes.Contains(refused, []byte("affinity")) || calls.Load() != 0 {
		t.Fatal("required policy did not refuse before dispatch")
	}
	if drain {
		p.request(t, "POST", "/api/hosts/source/maintenance/enter", []byte(`{"drain":true,"mode":"live"}`), 200)
		deadline := time.Now().Add(10 * time.Second)
		for calls.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if calls.Load() != 1 {
			t.Fatal("drain did not select permitted target")
		}
	} else {
		p.request(t, "POST", "/api/command", request("192.0.2.3"), 200)
	}
	if calls.Load() != 1 {
		t.Fatal("accepted placement dispatch count")
	}
	blocked := func() {
		t.Helper()
		body := p.request(t, "POST", "/api/command", request("192.0.2.3"), 409)
		if !bytes.Contains(body, []byte("reservation")) {
			t.Fatal("reservation refusal missing")
		}
		if calls.Load() != 1 {
			t.Fatal("reservation allowed replay")
		}
	}
	blocked()
	p.stop(t, false)
	p = startAcceptance(t, binary, root, b, token, false)
	blocked()
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restored := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restored, 0)
	if snapshot.Affinity == nil || snapshot.Affinity.Reservation == nil || snapshot.Affinity.Version != 2 || snapshot.Affinity.ReservationPhase != "dispatch_possible" || len(snapshot.Affinity.Aborted) != 1 || !reflect.DeepEqual(snapshot.Affinity.Originals, state.Originals) {
		t.Fatal("backup lost original rules or reservation")
	}
	want := snapshot.Affinity.Clone()
	b = backend(restored)
	p = startAcceptance(t, binary, restored, b, token, false)
	blocked()
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	if !reflect.DeepEqual(second.Affinity, want) {
		t.Fatal("second backup changed reservation/originals")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restored, b, token, false)
	blocked()
	p.stop(t, false)
	for _, pair := range [][2]string{{"affinity_rules.bson", "original-affinity_rules.bson"}, {"affinity_rules.indexes.bson", "indexes-affinity_rules.bson"}} {
		a, e := os.ReadFile(filepath.Join(source, pair[0]))
		if e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(filepath.Join(stage, pair[1]))
		if e != nil || !bytes.Equal(a, b) {
			t.Fatal("original bytes changed")
		}
	}
	rawRestore := filepath.Join(root, "raw-restored")
	if err = os.Mkdir(rawRestore, 0700); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"source.json", "snapshot.json"}, {"original-affinity_rules.bson", "affinity_rules.bson"}, {"indexes-affinity_rules.bson", "affinity_rules.indexes.bson"}} {
		raw, e := os.ReadFile(filepath.Join(stage, pair[0]))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(rawRestore, pair[1]), raw, 0400); e != nil {
			t.Fatal(e)
		}
	}
	recovered := filepath.Join(root, "recovered")
	run("-mode", "convert", "-source", rawRestore, "-bundle", recovered)
	if !bytes.Equal(export, run("-mode", "legacy-affinity-state", "-bundle", recovered)) {
		t.Fatal("raw restore/reconversion differs")
	}
	kind := "affinity_placement_lifecycle"
	if drain {
		kind = "affinity_drain_lifecycle"
	}
	emitAcceptance(t, kind, map[string]any{"backend": mode, "fixture_schema": fixture.Schema, "source_contract": fixture.Source, "original_sha256": fixture.Checksums["affinity_rules.bson"], "index_sha256": fixture.Checksums["affinity_rules.indexes.bson"], "required_refusal_before_dispatch": true, "accepted_synthetic_dispatch": true, "durable_unresolved_reservation": true, "replay_refused": true, "restart": true, "application_backup_fresh_restore": true, "second_backup_restart": true, "originals_unchanged": true, "raw_restore_reconversion": true, "reservation_reconciliation": false, "predispatch_abort_retained": true, "aborted_replay_refused": true, "new_identity_after_abort": true, "affinity_state_version": 2, "name_or_tag_selectors": false, "native_migration_success": false, "full_legacy_compatible": false})
}
