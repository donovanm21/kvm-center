package main

import (
	"kvm-center/internal/models"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestCancellationRequestedPreservesPending(t *testing.T) {
	for _, tc := range []struct {
		state, want string
		status      int
	}{{"cancellation_requested", "pending", 202}, {"running", "pending", 202}, {"cancelled", "cancelled", 200}, {"success", "success", 200}, {"invalid", "pending", 502}} {
		t.Run(tc.state, func(t *testing.T) {
			s, _ := migrationFixture(t)
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"request_id":"cancel-fixture","state":"` + tc.state + `"}`))
			}))
			defer agent.Close()
			_, port, _ := net.SplitHostPort(strings.TrimPrefix(agent.URL, "http://"))
			s.hosts["source"].AgentPort, _ = strconv.Atoi(port)
			s.commandLog.Record(&models.CommandLogEntry{RequestID: "cancel-fixture", HostID: "source", VMUUID: "fixture-uuid", VMName: "fixture", Action: "START", Outcome: "pending"})
			w := httptest.NewRecorder()
			handleCommandCancel(s)(w, httptest.NewRequest(http.MethodPost, "/api/command/cancel?request_id=cancel-fixture", strings.NewReader(`{"reason":"test"}`)))
			if w.Code != tc.status {
				t.Fatalf("status%d %s", w.Code, w.Body)
			}
			var outcome string
			for _, e := range s.commandLog.List(0) {
				if e.RequestID == "cancel-fixture" {
					outcome = e.Outcome
				}
			}
			if outcome != tc.want {
				t.Fatalf("outcome%s want%s", outcome, tc.want)
			}
			if tc.want == "pending" {
				events, _ := s.GetAuditEvents(AuditFilter{})
				for _, e := range events {
					if e.Action == "command.cancelled" || e.Action == "command.cancellation_result" {
						t.Fatalf("premature completion audit %+v", e)
					}
				}
			}
		})
	}
}
