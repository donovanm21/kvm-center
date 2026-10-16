package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func migrationFixture(t *testing.T) (*Store, *models.User) {
	t.Helper()
	s := newTestStore(t)
	u := &models.User{ID: "migration-operator", RoleIDs: []string{"migration-role"}, Enabled: true}
	s.roles["migration-role"] = &models.Role{ID: "migration-role", Permissions: []string{"vm.migrate"}}
	s.users[u.ID] = u
	s.hosts["source"] = &models.Host{ID: "source", IP: "127.0.0.1", Status: models.HostOnline, LastSeen: time.Now()}
	s.vms["source"] = []*models.VM{{UUID: "fixture-uuid", Name: "fixture-vm", HostID: "source", State: models.VMRunning}}
	return s, u
}
func migrationRequest(s *Store, u *models.User, req models.APICommandRequest) *httptest.ResponseRecorder {
	b, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/api/command", bytes.NewReader(b))
	r = r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
	w := httptest.NewRecorder()
	handleCommand(s)(w, r)
	return w
}
func TestLiveMigrationSnapshotBoundaries(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name             string
		age              time.Duration
		state            models.VMState
		offline, missing bool
		reqName          string
		ok               bool
	}{
		{name: "running", state: models.VMRunning, reqName: "fixture-vm", ok: true},
		{name: "boundary", age: 2 * time.Minute, state: models.VMRunning, ok: true},
		{name: "stale", age: 2*time.Minute + time.Nanosecond, state: models.VMRunning},
		{name: "stopped", state: models.VMStopped}, {name: "unknown", state: models.VMUnknown},
		{name: "paused", state: models.VMPaused}, {name: "offline", state: models.VMRunning, offline: true},
		{name: "missing", state: models.VMRunning, missing: true},
		{name: "identity mismatch", state: models.VMRunning, reqName: "different-vm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := migrationFixture(t)
			s.hosts["source"].LastSeen = now.Add(-tc.age)
			s.vms["source"][0].State = tc.state
			if tc.offline {
				s.hosts["source"].Status = models.HostOffline
			}
			if tc.missing {
				delete(s.vms, "source")
			}
			err := s.liveMigrationSource(models.APICommandRequest{HostID: "source", VMUUID: "fixture-uuid", VMName: tc.reqName}, now)
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%v err=%v", err == nil, err)
			}
		})
	}
}
func TestLiveMigrationAPIAdmissionAndOfflineSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, action   string
		state          models.VMState
		stale, offline bool
		want           int
	}{
		{"live-running", "MIGRATE", models.VMRunning, false, false, 200},
		{"live-stopped", "MIGRATE", models.VMStopped, false, false, 409},
		{"live-unknown", "MIGRATE", models.VMUnknown, false, false, 409},
		{"live-stale", "MIGRATE", models.VMRunning, true, false, 409},
		{"live-offline", "MIGRATE", models.VMRunning, false, true, 409},
		{"offline-stopped", "OFFLINE_MIGRATE", models.VMStopped, false, false, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, u := migrationFixture(t)
			var calls atomic.Int32
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/command" {
					calls.Add(1)
					var req models.CommandRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					if req.Action != tc.action {
						t.Errorf("action changed: %s", req.Action)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"message":"synthetic completion","active":false}`))
			}))
			defer agent.Close()
			_, port, _ := net.SplitHostPort(strings.TrimPrefix(agent.URL, "http://"))
			s.hosts["source"].AgentPort, _ = strconv.Atoi(port)
			s.vms["source"][0].State = tc.state
			if tc.stale {
				s.hosts["source"].LastSeen = time.Now().Add(-3 * time.Minute)
			}
			if tc.offline {
				s.hosts["source"].Status = models.HostOffline
			}
			req := models.APICommandRequest{RequestID: "fixture-request", Action: tc.action, HostID: "source", VMUUID: "fixture-uuid", VMName: "fixture-vm", Params: map[string]string{"destination": "192.0.2.20"}}
			w := migrationRequest(s, u, req)
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if tc.want == 409 {
				if calls.Load() != 0 || len(s.GetMigrations()) != 0 {
					t.Fatal("rejected request dispatched or created migration")
				}
				if s.commandLog.List(1)[0].Outcome != "failed" {
					t.Fatal("rejection left pending task")
				}
			} else {
				if calls.Load() != 1 {
					t.Fatal("allowed request not dispatched")
				}
				s.mu.Lock()
				s.vms["source"][0].State = models.VMStopped
				s.hosts["source"].Status = models.HostOffline
				s.mu.Unlock()
				replay := migrationRequest(s, u, req)
				if replay.Code != 200 || replay.Header().Get("X-Command-Replayed") != "true" || calls.Load() != 1 {
					t.Fatalf("terminal replay changed: %d %s", replay.Code, replay.Body)
				}
			}
		})
	}
}
func TestLiveMigrationPendingReplayPreservesUncertainty(t *testing.T) {
	s, u := migrationFixture(t)
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("pending retry must not dispatch again")
		w.WriteHeader(202)
	}))
	defer agent.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(agent.URL, "http://"))
	s.hosts["source"].AgentPort, _ = strconv.Atoi(port)
	s.vms["source"][0].State = models.VMStopped
	s.commandLog.Record(&models.CommandLogEntry{RequestID: "pending", Action: "MIGRATE", VMUUID: "fixture-uuid", HostID: "source", Outcome: "pending", SubmittedAt: time.Now()})
	w := migrationRequest(s, u, models.APICommandRequest{RequestID: "pending", Action: "MIGRATE", HostID: "source", VMUUID: "fixture-uuid", VMName: "fixture-vm", Params: map[string]string{"destination": "192.0.2.20"}})
	if w.Code != 202 || s.commandLog.List(1)[0].Outcome != "pending" {
		t.Fatalf("uncertain outcome rewritten: %d %s", w.Code, w.Body)
	}
}
