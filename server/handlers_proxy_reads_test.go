package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kvm-center/internal/agentauth"
	"kvm-center/internal/models"
)

// Real login/session middleware and signed HTTP requests to a synthetic agent.
// This reproduces error-origin confusion; it does not diagnose a live host.
func TestProxyReadErrorsPreserveAuthenticatedSession(t *testing.T) {
	for _, route := range []string{"/api/storage/browse?host=synthetic-host&path=%2Fsynthetic", "/api/vms/snapshots?host=synthetic-host&vm=synthetic-vm"} {
		t.Run(strings.Split(route, "?")[0], func(t *testing.T) {
			h := newIntegrationHarness(t)
			h.registerFirstAdmin("synthetic-admin", "synthetic-fixture-password")
			secret := h.Store.GetOrCreateConsoleSecret()
			var status atomic.Int32
			status.Store(200)
			var calls atomic.Int32
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if err := agentauth.Verify(secret, "synthetic-host", r.Method, r.URL.Path, r.Header.Get(agentauth.HeaderAuth), r.Header.Get(agentauth.HeaderExp), r.Header.Get(agentauth.HeaderNonce), time.Now()); err != nil {
					t.Error("fixture request signature invalid")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(int(status.Load()))
				if status.Load() == 200 {
					fmt.Fprint(w, `{"synthetic":"ok"}`)
				} else {
					fmt.Fprint(w, `{"error":"fixture upstream rejection details"}`)
				}
			}))
			defer agent.Close()
			host, portText, err := net.SplitHostPort(strings.TrimPrefix(agent.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			port, _ := strconv.Atoi(portText)
			h.Store.mu.Lock()
			h.Store.hosts["synthetic-host"] = &models.Host{ID: "synthetic-host", IP: host, AgentPort: port}
			h.Store.mu.Unlock()
			for _, upstream := range []int{200, 401, 403, 500} {
				status.Store(int32(upstream))
				code, body := h.do("GET", route, nil)
				expected := upstream
				if upstream == 401 || upstream == 403 {
					expected = 502
				}
				if code != expected {
					t.Fatalf("upstream=%d response=%d body=%s", upstream, code, body)
				}
				if expected == 502 && (!strings.Contains(string(body), "agent rejected the server request") || strings.Contains(string(body), "fixture upstream rejection details")) {
					t.Fatalf("wrong failure origin: %s", body)
				}
				if code, _ := h.do("GET", "/api/auth/me", nil); code != 200 {
					t.Fatal("valid browser session lost")
				}
			}
			before := calls.Load()
			if strings.HasPrefix(route, "/api/storage/") {
				h.Store.mu.Lock()
				for _, u := range h.Store.users {
					u.RoleIDs = nil
					u.GroupIDs = nil
				}
				h.Store.mu.Unlock()
				if code, _ := h.do("GET", route, nil); code != 403 {
					t.Fatal("storage permission denial changed")
				}
				if calls.Load() != before {
					t.Fatal("permission-denied request reached agent")
				}
			}
			h.do("POST", "/api/auth/logout", nil)
			if code, _ := h.do("GET", route, nil); code != 401 {
				t.Fatal("unauthenticated request admitted")
			}
			if calls.Load() != before {
				t.Fatal("unauthenticated request reached agent")
			}
		})
	}
}
