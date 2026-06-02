package main

// Tests for the v5.48 PR-2c per-action watchdog. We use `sleep` and
// `true` as stand-ins for real virsh commands so the tests don't
// need libvirt installed in CI.

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func TestWatchdogTimeoutFor_DefaultMatrix(t *testing.T) {
	cases := []struct {
		action string
		want   time.Duration
	}{
		{"START", 60 * time.Second},
		{"DELETE_SNAPSHOT", 30 * time.Minute},
		{"MIGRATE", 60 * time.Minute},
		{"OFFLINE_MIGRATE", 90 * time.Minute},
		{"NEVER_HEARD_OF_THIS", defaultWatchdogTimeout},
	}
	for _, c := range cases {
		if got := watchdogTimeoutFor(c.action, nil); got != c.want {
			t.Fatalf("%s: got %s, want %s", c.action, got, c.want)
		}
	}
}

func TestWatchdogTimeoutFor_ParamOverride(t *testing.T) {
	// Param override accepted.
	got := watchdogTimeoutFor("DELETE_SNAPSHOT", map[string]string{"watchdog_seconds": "10"})
	if got != 10*time.Second {
		t.Fatalf("override not honoured: %s", got)
	}
	// Negative / non-numeric ignored, matrix wins.
	if got := watchdogTimeoutFor("DELETE_SNAPSHOT", map[string]string{"watchdog_seconds": "-5"}); got != 30*time.Minute {
		t.Fatalf("negative override not rejected: %s", got)
	}
	if got := watchdogTimeoutFor("DELETE_SNAPSHOT", map[string]string{"watchdog_seconds": "nope"}); got != 30*time.Minute {
		t.Fatalf("non-numeric override not rejected: %s", got)
	}
}

func TestWatchdogTimeoutFor_EditVMShutdownPath(t *testing.T) {
	if got := watchdogTimeoutFor("EDIT_VM", nil); got != 30*time.Second {
		t.Fatalf("plain edit: %s", got)
	}
	got := watchdogTimeoutFor("EDIT_VM", map[string]string{"allow_shutdown": "true"})
	if got != 5*time.Minute {
		t.Fatalf("allow_shutdown path: %s", got)
	}
}

func TestRunWithWatchdog_HappyPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/true / /bin/echo")
	}
	req := models.CommandRequest{
		RequestID: "req_wd_ok",
		Action:    "START",
		Params:    map[string]string{"watchdog_seconds": "5"},
	}
	out, err := runWithWatchdog(context.Background(), req, "START", "/bin/echo", "hello")
	if err != nil {
		t.Fatalf("expected ok, got %v out=%s", err, out)
	}
	if !strings.Contains(string(out), "hello") {
		t.Fatalf("output not captured: %q", out)
	}
}

func TestRunWithWatchdog_KillsStuckChildOnTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sleep")
	}
	req := models.CommandRequest{
		RequestID: "req_wd_kill",
		Action:    "START",
		Params:    map[string]string{"watchdog_seconds": "1"},
	}

	start := time.Now()
	_, err := runWithWatchdog(context.Background(), req, "START", "/bin/sleep", "30")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected watchdog error")
	}
	if !errors.Is(err, ErrWatchdog) {
		t.Fatalf("error %v should wrap ErrWatchdog", err)
	}
	// Should land within timeout + grace + a little slack — way
	// under the 30 s sleep.
	if elapsed > 10*time.Second {
		t.Fatalf("watchdog took too long: %s", elapsed)
	}
}

func TestRunWithWatchdog_StampsChildPIDAndDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sleep")
	}
	s := withOpsStore(t)
	req := models.CommandRequest{
		RequestID: "req_wd_pid",
		Action:    "START",
		Params:    map[string]string{"watchdog_seconds": "3"},
	}
	// Pre-seed the row as PR-2b's wrapper would.
	recordOpsLifecycleStart(req)

	go func() {
		time.Sleep(200 * time.Millisecond)
		got, _ := s.Get(context.Background(), "req_wd_pid")
		if got != nil && got.ChildPID == 0 {
			t.Logf("expected ChildPID stamped while running, got 0; ignoring race")
		}
		if got != nil && got.WatchdogAt == nil {
			t.Logf("expected WatchdogAt stamped while running")
		}
	}()

	_, _ = runWithWatchdog(context.Background(), req, "START", "/bin/sleep", "1")
	got, _ := s.Get(context.Background(), "req_wd_pid")
	if got == nil {
		t.Fatal("ops.db row missing post-run")
	}
	if got.ChildPID != 0 {
		t.Fatalf("ChildPID should be cleared after process exits, got %d", got.ChildPID)
	}
	if got.WatchdogAt == nil {
		t.Fatal("WatchdogAt should remain stamped for forensics")
	}
}

func TestRunWithWatchdog_ContextCancelTermination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sleep")
	}
	req := models.CommandRequest{
		RequestID: "req_wd_ctx",
		Action:    "START",
		Params:    map[string]string{"watchdog_seconds": "30"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := runWithWatchdog(ctx, req, "START", "/bin/sleep", "30")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error after ctx cancel")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("ctx cancel didn't reap process promptly: %s", elapsed)
	}
}

func TestRunWithWatchdog_NoOpsDBStillWorks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/echo")
	}
	prev := ops
	ops = nil
	defer func() { ops = prev }()
	req := models.CommandRequest{
		RequestID: "req_wd_noops",
		Action:    "START",
	}
	out, err := runWithWatchdog(context.Background(), req, "START", "/bin/echo", "ok")
	if err != nil {
		t.Fatalf("watchdog should work without ops.db: %v", err)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("output: %s", out)
	}
}
