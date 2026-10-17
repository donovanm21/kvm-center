package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"kvm-center/internal/models"
)

func TestCommandAuditMatchesObservedOutcome(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body, outcome string
		success       bool
	}{
		{"nonowned rejection", 403, `{"success":false,"message":"not owned"}`, "failed", false},
		{"successful command", 200, `{"success":true,"message":"done"}`, "success", true},
		{"application failure", 200, `{"success":false,"message":"already active"}`, "failed", false},
		{"truncated response", 200, `{"success":true}`, "pending", false},
		{"async", 202, `{"success":true,"message":"accepted"}`, "pending", false},
		{"unknown server error", 500, `unavailable`, "pending", false},
		{"rejection without flag", 409, `{"message":"conflict"}`, "failed", false},
		{"contradictory HTTP refusal", 403, `{"success":true}`, "failed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, u := migrationFixture(t)
			s.roles["migration-role"].Permissions = []string{models.PermVMLifecycle}
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.name == "truncated response" {
					w.Header().Set("Content-Length", "1000")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer agent.Close()
			addr := strings.TrimPrefix(agent.URL, "http://")
			_, port, _ := net.SplitHostPort(addr)
			s.hosts["source"].AgentPort, _ = strconv.Atoi(port)
			req := models.APICommandRequest{HostID: "source", VMUUID: "fixture-uuid", Action: "START", RequestID: "audit-fixture"}
			w := migrationRequest(s, u, req)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			events, _ := s.GetAuditEvents(AuditFilter{})
			count := 0
			for _, e := range events {
				if e.Action == AuditVMAction {
					count++
					if e.Success != tc.success || e.Detail["outcome"] != tc.outcome || e.Detail["request_id"] != req.RequestID {
						t.Fatalf("audit mismatch: %+v", e)
					}
					if !e.Success && e.Error == "" {
						t.Fatal("missing outcome explanation")
					}
				}
			}
			if count != 1 {
				t.Fatalf("audit count %d", count)
			}
		})
	}
}
func TestCommandAuditEarlyRefusalAndCachedOutcome(t *testing.T) {
	for _, outcome := range []string{"early", "success", "failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			s, u := migrationFixture(t)
			s.roles["migration-role"].Permissions = []string{models.PermVMLifecycle}
			s.hosts["source"].AgentPort = 1
			req := models.APICommandRequest{HostID: "source", VMUUID: "fixture-uuid", Action: "START", RequestID: "cached-audit"}
			if outcome == "early" {
				req.HostID = "missing"
			} else {
				s.commandLog.Record(&models.CommandLogEntry{RequestID: req.RequestID, Action: "START", Outcome: outcome, StatusCode: 200})
			}
			migrationRequest(s, u, req)
			events, _ := s.GetAuditEvents(AuditFilter{})
			if len(events) != 1 || events[0].Success != (outcome == "success") {
				t.Fatalf("unexpected audit %+v", events)
			}
			expected := outcome
			if outcome == "early" {
				expected = "failed"
			}
			if events[0].Detail["outcome"] != expected {
				t.Fatal(events[0].Detail)
			}
		})
	}
}

func TestCommandAuditTransportUncertainty(t *testing.T) {
	s, u := migrationFixture(t)
	s.roles["migration-role"].Permissions = []string{models.PermVMLifecycle}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer agent.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(agent.URL, "http://"))
	s.hosts["source"].AgentPort, _ = strconv.Atoi(port)
	w := migrationRequest(s, u, models.APICommandRequest{HostID: "source", VMUUID: "fixture-uuid", Action: "START", RequestID: "transport-audit"})
	if w.Code != 502 {
		t.Fatal(w.Code)
	}
	events, _ := s.GetAuditEvents(AuditFilter{})
	if len(events) != 1 || events[0].Success || events[0].Detail["outcome"] != "pending" {
		t.Fatalf("uncertainty lost: %+v", events)
	}
}
func TestCommandAuditPendingLiveReplay(t *testing.T) {
	s, u := migrationFixture(t)
	s.hosts["source"].AgentPort = 1
	s.commandLog.Record(&models.CommandLogEntry{RequestID: "pending-audit", Action: "MIGRATE", HostID: "source", VMUUID: "fixture-uuid", Outcome: "pending"})
	w := migrationRequest(s, u, models.APICommandRequest{HostID: "source", VMUUID: "fixture-uuid", Action: "MIGRATE", RequestID: "pending-audit"})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	events, _ := s.GetAuditEvents(AuditFilter{})
	if len(events) != 1 || events[0].Success || events[0].Action != AuditMigrationStart || events[0].Detail["outcome"] != "pending" {
		t.Fatalf("pending replay audit: %+v", events)
	}
}
