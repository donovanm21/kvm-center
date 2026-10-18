package main

import (
	"context"
	"encoding/json"
	"errors"
	"kvm-center/internal/affinitypolicy"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
	"kvm-center/internal/offlinestaging"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func affinityFixtureState(t *testing.T) *affinitypolicy.State {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err = os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err = legacyfixture.Write(source, legacyfixture.GenerateAffinity()); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "stage")
	if _, err = offlinestaging.Convert(source, stage); err != nil {
		t.Fatal(err)
	}
	state, err := offlinestaging.ExportLegacyAffinityState(stage)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func affinityStore(t *testing.T) (*Store, *models.User, *atomic.Int32) {
	s, u := migrationFixture(t)
	s.affinity = affinityFixtureState(t)
	s.backend = NewJSONBackend(filepath.Join(t.TempDir(), "state.json"))
	s.saveGateOpen = true
	now := time.Now()
	s.hosts["peer"] = &models.Host{ID: "peer", IP: "192.0.2.2", Status: models.HostOnline, LastSeen: now, MemUsage: 1}
	s.hosts["target"] = &models.Host{ID: "target", IP: "192.0.2.3", Status: models.HostOnline, LastSeen: now, MemUsage: 50}
	s.vms["source"] = []*models.VM{{UUID: legacyfixture.VMID, Name: "one", HostID: "source", State: models.VMRunning}, {UUID: legacyfixture.AffinityVM3, Name: "three", HostID: "source", State: models.VMRunning}}
	s.vms["peer"] = []*models.VM{{UUID: legacyfixture.AffinityVM2, Name: "two", HostID: "peer", State: models.VMRunning}}
	calls := &atomic.Int32{}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/command" {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"active":false}`))
	}))
	t.Cleanup(agent.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(agent.URL, "http://"))
	s.hosts["source"].AgentPort, _ = strconv.Atoi(port)
	return s, u, calls
}
func affinityRequest(dest string) models.APICommandRequest {
	return models.APICommandRequest{RequestID: "affinity-request", Action: "MIGRATE", HostID: "source", VMUUID: legacyfixture.VMID, VMName: "one", Params: map[string]string{"destination": dest}}
}
func TestAffinityMigrationEnforcementRecovery(t *testing.T) {
	oldSigner := serverGrantSigner
	signer, err := EnsureGrantSigner(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	serverGrantSigner = signer
	defer func() { serverGrantSigner = oldSigner }()
	for _, action := range []string{"MIGRATE", "OFFLINE_MIGRATE"} {
		t.Run(action, func(t *testing.T) {
			s, u, calls := affinityStore(t)
			req := affinityRequest("192.0.2.2")
			req.Action = action
			req.Params["migration_transport"] = "tls"
			w := migrationRequest(s, u, req)
			if w.Code != 409 || calls.Load() != 0 || len(s.GetMigrations()) != 0 || len(s.commandLog.List(0)) != 0 {
				t.Fatalf("refusal had side effects: %d %s", w.Code, w.Body)
			}
			events, _ := s.GetAuditEvents(AuditFilter{})
			for _, e := range events {
				if e.Action == AuditMigrationGrantIssue {
					t.Fatal("denied affinity minted grant")
				}
			}
			req.Params["destination"] = "192.0.2.3"
			w = migrationRequest(s, u, req)
			if w.Code != 200 || calls.Load() != 1 || w.Header().Get("X-Affinity-Warnings") == "" {
				t.Fatalf("accepted move: %d %s", w.Code, w.Body)
			}
			replay := migrationRequest(s, u, req)
			if replay.Code != 409 || calls.Load() != 1 {
				t.Fatal("reservation permitted replay")
			}
			s.saveWG.Wait()
			if err := s.SaveChecked(); err != nil {
				t.Fatal(err)
			}
			d, err := s.backend.Load(context.Background())
			if err != nil || d.Affinity.Reservation == nil {
				t.Fatal("reservation not durable", err)
			}
			raw, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			schemas, err := snapshotFeatureSchemas(raw)
			if err != nil || schemas["affinity_state"] != 1 {
				t.Fatal("missing archive schema", err)
			}
			restored := newTestStore(t)
			restored.backend = NewJSONBackend(filepath.Join(t.TempDir(), "state.json"))
			if err = restored.backend.Save(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			restored.load()
			if !reflect.DeepEqual(restored.affinity, d.Affinity) {
				t.Fatal("restore changed originals/reservation")
			}
			if w = migrationRequest(restored, u, req); w.Code != 409 || calls.Load() != 1 {
				t.Fatal("restore permitted dispatch")
			}
		})
	}
}
func TestAffinityDrainFilteringAndReservation(t *testing.T) {
	s, _, calls := affinityStore(t)
	vm := s.vms["source"][0]
	target, why := pickDrainTargetReason(s, "source", vm)
	if target == nil || target.ID != "target" {
		t.Fatal(target, why)
	}
	if err := drainOneVM(context.Background(), s, "source", vm, s.hosts["peer"], "live"); err == nil || calls.Load() != 0 {
		t.Fatal("drain violated must")
	}
	if err := drainOneVM(context.Background(), s, "source", vm, target, "live"); err != nil || calls.Load() != 1 {
		t.Fatal("allowed drain failed", err)
	}
	if target, why = pickDrainTargetReason(s, "source", s.vms["source"][1]); target != nil || !strings.Contains(why, "reservation") {
		t.Fatal("pending drain not explicitly unplaceable", why)
	}
	if err := drainOneVM(context.Background(), s, "source", vm, s.hosts["target"], "offline"); err == nil || calls.Load() != 1 {
		t.Fatal("drain reservation bypassed")
	}
}

type affinityFailBackend struct{ Backend }

func (b affinityFailBackend) Save(context.Context, *persistData) error {
	return errors.New("synthetic save failure")
}
func TestAffinityUncertaintyAndConcurrency(t *testing.T) {
	t.Run("save failure", func(t *testing.T) {
		s, u, calls := affinityStore(t)
		s.backend = affinityFailBackend{s.backend}
		w := migrationRequest(s, u, affinityRequest("192.0.2.3"))
		if w.Code != 409 || calls.Load() != 0 || s.affinity.Reservation == nil {
			t.Fatal("save failure dispatched or released")
		}
	})
	t.Run("simultaneous", func(t *testing.T) {
		s, u, calls := affinityStore(t)
		var wg sync.WaitGroup
		codes := make(chan int, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); codes <- migrationRequest(s, u, affinityRequest("192.0.2.3")).Code }()
		}
		wg.Wait()
		close(codes)
		ok, blocked := 0, 0
		for c := range codes {
			if c == 200 {
				ok++
			}
			if c == 409 {
				blocked++
			}
		}
		if ok != 1 || blocked != 1 || calls.Load() != 1 {
			t.Fatal("concurrent placement escaped", ok, blocked, calls.Load())
		}
	})
	for _, kind := range []string{"stale", "duplicate", "unknown destination", "conflicting request", "old pending", "host id destination", "duplicate IP", "conflicting host field"} {
		t.Run(kind, func(t *testing.T) {
			s, u, calls := affinityStore(t)
			req := affinityRequest("192.0.2.3")
			switch kind {
			case "host id destination":
				req.Params["destination"] = "target"
			case "duplicate IP":
				s.hosts["peer"].IP = s.hosts["target"].IP
			case "conflicting host field":
				s.vms["peer"][0].HostID = "source"
			case "stale":
				s.hosts["peer"].LastSeen = time.Now().Add(-3 * time.Minute)
			case "duplicate":
				s.vms["peer"][0].Name = "one"
			case "unknown destination":
				req.Params["destination"] = "unknown"
			case "conflicting request":
				req.VMName = "two"
			case "old pending":
				s.commandLog.Record(&models.CommandLogEntry{RequestID: "old", Action: "OFFLINE_MIGRATE", Outcome: "pending"})
			}
			if w := migrationRequest(s, u, req); w.Code != 409 || calls.Load() != 0 || s.affinity.Reservation != nil {
				t.Fatal("unsafe admission", w.Code, w.Body)
			}
		})
	}
}

func TestAffinityCommandParamsOwnedByLog(t *testing.T) {
	log := NewCommandLog()
	params := map[string]string{"destination": "192.0.2.3"}
	returned, _ := log.Record(&models.CommandLogEntry{RequestID: "copy", Params: params})
	params["migration_id"] = "later"
	returned.Params["destination"] = "changed"
	entry := log.snapshot()["copy"]
	if entry.Params["migration_id"] != "" || entry.Params["destination"] != "192.0.2.3" {
		t.Fatal("command log aliases caller params")
	}
}
