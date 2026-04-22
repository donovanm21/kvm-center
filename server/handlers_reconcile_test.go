package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// TestReconcile_NoAgentAddress: a host without an agent address
// returns the snapshot with AgentStatus=skipped and no crash.
func TestReconcile_NoAgentAddress(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	s.hosts["hv1"] = &models.Host{ID: "hv1", LastSeen: time.Now()}
	// No agent IP configured.

	req := httptest.NewRequest(http.MethodPost, "/api/admin/reconcile/hv1", nil)
	rr := httptest.NewRecorder()
	handleReconcileHost(s)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var rep reconcileReport
	if err := json.Unmarshal(rr.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rep.AgentStatus != "skipped" {
		t.Fatalf("agent_status = %q, want skipped", rep.AgentStatus)
	}
	if rep.HostID != "hv1" {
		t.Fatalf("host_id = %q", rep.HostID)
	}
}

// TestReconcile_AgentUnreachable: agent endpoint 500s → report
// comes back with AgentStatus=unreachable + populated error.
func TestReconcile_AgentUnreachable(t *testing.T) {
	// Stand up a fake agent that always errors.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer fake.Close()
	addr := strings.TrimPrefix(fake.URL, "http://")

	s := NewStore(t.TempDir() + "/data.json")
	s.hosts["hv1"] = &models.Host{ID: "hv1", IP: strings.Split(addr, ":")[0], AgentPort: intFromAddr(addr), LastSeen: time.Now()}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/reconcile/hv1", nil)
	rr := httptest.NewRecorder()
	handleReconcileHost(s)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var rep reconcileReport
	_ = json.Unmarshal(rr.Body.Bytes(), &rep)
	if rep.AgentStatus != "unreachable" {
		t.Fatalf("agent_status = %q (err=%q), want unreachable", rep.AgentStatus, rep.AgentError)
	}
}

// TestReconcile_MissingHost: unknown host id → 404.
func TestReconcile_MissingHost(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	req := httptest.NewRequest(http.MethodPost, "/api/admin/reconcile/absent", nil)
	rr := httptest.NewRecorder()
	handleReconcileHost(s)(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// TestIndexByUUID_FallsBackToName: VMs without a UUID fall back to
// name-based indexing so the diff still works on pre-v4 records.
func TestIndexByUUID_FallsBackToName(t *testing.T) {
	vms := []vmBrief{
		{UUID: "", Name: "legacy", State: models.VMRunning},
		{UUID: "UUID-1", Name: "modern", State: models.VMStopped},
	}
	idx := indexByUUID(vms)
	if _, ok := idx["legacy"]; !ok {
		t.Fatal("name fallback key missing")
	}
	if _, ok := idx["uuid-1"]; !ok {
		t.Fatal("UUID key missing (expected lowercase)")
	}
}

func intFromAddr(addr string) int {
	// addr is "host:port"
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			n := 0
			for _, c := range addr[i+1:] {
				if c < '0' || c > '9' {
					return 0
				}
				n = n*10 + int(c-'0')
			}
			return n
		}
	}
	return 0
}
