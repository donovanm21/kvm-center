package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func TestLiveMigrationExecutionChecks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []string
		err    error
		want   int
		grants int
	}{
		{"running", []string{"running", "running"}, nil, 200, 1},
		{"stopped", []string{"shut off"}, nil, 409, 0},
		{"unknown", []string{"unknown"}, nil, 409, 0},
		{"empty", []string{""}, nil, 409, 0},
		{"unavailable", nil, errors.New("synthetic unavailable"), 409, 0},
		{"timeout", nil, context.DeadlineExceeded, 409, 0},
		{"changed-during-grant", []string{"running", "shut off"}, nil, 409, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := sshStrictMode.Load()
			t.Cleanup(func() { setSSHStrict(prior) })
			reads, grants, migrations := 0, 0, 0
			read := func(ctx context.Context, target string) (string, error) {
				if target != "fixture-vm" {
					t.Fatal("wrong target")
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("unbounded probe")
				}
				if tc.err != nil {
					return "", tc.err
				}
				state := tc.states[reads]
				reads++
				return state, nil
			}
			w := httptest.NewRecorder()
			handleMigrateWithChecks(w, models.CommandRequest{Action: "MIGRATE", Params: map[string]string{"destination": "192.0.2.20", "migration_transport": "tls", "grant": "synthetic-grant"}}, "fixture-vm", read,
				func(string, string) error { grants++; return nil }, func(args []string, _, _, _ string) ([]byte, error) {
					migrations++
					if !strings.Contains(strings.Join(args, " "), "--live") {
						t.Fatal("live semantics lost")
					}
					return nil, nil
				})
			if w.Code != tc.want || grants != tc.grants || (migrations == 1) != (tc.want == 200) {
				t.Fatalf("status=%d grants=%d migrations=%d body=%s", w.Code, grants, migrations, w.Body)
			}
		})
	}
}
func TestLiveMigrationRechecksAfterDispatchWaitAndCachesRejection(t *testing.T) {
	resetOutcomes(t)
	withOpsStore(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan *httptest.ResponseRecorder, 1)
	var stopped atomic.Bool
	reads, migrations := 0, 0
	req := models.CommandRequest{RequestID: "fixture-queued-live", Action: "MIGRATE", VMName: "fixture-vm", Params: map[string]string{"destination": "192.0.2.20"}}
	execute := func(w http.ResponseWriter, r models.CommandRequest) {
		close(started)
		<-release
		handleMigrateWithChecks(w, r, r.VMName, func(context.Context, string) (string, error) {
			reads++
			if stopped.Load() {
				return "shut off", nil
			}
			return "running", nil
		}, func(string, string) error { t.Error("unexpected grant"); return nil }, func([]string, string, string, string) ([]byte, error) { migrations++; return nil, nil })
	}
	go func() { done <- dispatchForTest(req, execute) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("did not reach dispatch wait")
	}
	stopped.Store(true)
	close(release)
	select {
	case w := <-done:
		if w.Code != 409 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not return")
	}
	replay := dispatchForTest(req, func(http.ResponseWriter, models.CommandRequest) { t.Fatal("rejection executed twice") })
	if replay.Code != 409 || replay.Header().Get("X-Command-Replayed") != "true" || reads != 1 || migrations != 0 {
		t.Fatal("queued rejection not preserved")
	}
}
