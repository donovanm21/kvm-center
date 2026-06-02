package main

// Per-action watchdog (v5.48 PR-2c).
//
// Every command that shells out to virsh / qemu-img runs under a
// watchdog with an action-specific timeout. On timeout the agent
// SIGTERMs the child, waits 5 s, SIGKILLs. The ops.db row is
// updated with `state=failed` and `error="watchdog: …"`.
//
// Why this matters: the v5.48 design explicitly addresses the
// 2026-04-30 host-a incident where virsh and qemu-img both
// stalled inside libvirtd well beyond the server's 30-min HTTP
// timeout. Without a watchdog the only recourse was SSH + kill -9,
// and the bookkeeping diverged. With the watchdog the agent owns
// the process tree and surfaces the timeout cleanly.
//
// Operator override: the request body can include
// `params.watchdog_seconds=<int>` to bump the cap per-call. Useful
// for offline migrations of huge VMs on slow links.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kvm-center/internal/models"
)

const (
	// watchdogGraceSeconds is the SIGTERM → SIGKILL grace window.
	// Long enough for virsh to finalise + flush, short enough that
	// we don't sit forever on a process that's not responding.
	watchdogGraceSeconds = 5
	// defaultWatchdogTimeout is the cap for any action not in the
	// matrix below — also the floor for the override param so a
	// caller can't set "watchdog_seconds=0" and disable it entirely.
	defaultWatchdogTimeout = 60 * time.Second
)

// watchdogTimeoutFor returns the per-action timeout cap. Reads
// `params.watchdog_seconds` first; falls back to the matrix.
func watchdogTimeoutFor(action string, params map[string]string) time.Duration {
	if v := params["watchdog_seconds"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	switch strings.ToUpper(action) {
	case "START", "STOP", "RESTART", "FORCESTOP":
		return 60 * time.Second
	case "POWER_CYCLE":
		return 90 * time.Second
	case "SNAPSHOT":
		return 5 * time.Minute
	case "DELETE_SNAPSHOT":
		return 30 * time.Minute
	case "REVERT_SNAPSHOT":
		return 5 * time.Minute
	case "MIGRATE":
		return 60 * time.Minute
	case "OFFLINE_MIGRATE":
		return 90 * time.Minute
	case "EDIT_VM":
		// EDIT_VM defaults to a quick XML edit. Allow-shutdown path
		// (params.allow_shutdown=true) gets the longer cap because
		// it includes an ACPI shutdown + start.
		if params["allow_shutdown"] == "true" {
			return 5 * time.Minute
		}
		return 30 * time.Second
	case "CHANGE_MEDIA":
		return 30 * time.Second
	case "DELETE_VM":
		return 60 * time.Second
	}
	return defaultWatchdogTimeout
}

// ErrWatchdog is returned when the watchdog kills a child process.
// Wrapped errors carry the original exec error (if any) for log
// forensics.
var ErrWatchdog = errors.New("watchdog killed child")

// runWithWatchdog runs a virsh / qemu-img command under the
// watchdog. The request's request_id is used to stamp child_pid +
// watchdog_at in ops.db so a boot reconciler (PR-2d) can recover
// from an agent crash mid-execution. Returns the combined
// stdout+stderr and a wrapped error on watchdog kill.
//
// On timeout: SIGTERM child, wait watchdogGraceSeconds, SIGKILL,
// drain Wait. Pdeathsig=SIGKILL ensures the child also dies if the
// agent itself is killed mid-action without running our defers.
func runWithWatchdog(ctx context.Context, req models.CommandRequest, action string, name string, args ...string) ([]byte, error) {
	timeout := watchdogTimeoutFor(action, req.Params)
	deadline := time.Now().Add(timeout)

	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	configureProcessAttr(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	pid := cmd.Process.Pid

	// Stamp the child PID + deadline so the boot reconciler can
	// reattach (PR-2d) or surface "watchdog deadline" to the
	// operator. Best-effort.
	if ops != nil && req.RequestID != "" {
		dbCtx, cancel := context.WithTimeout(context.Background(), opsLookupDeadline)
		_ = ops.UpdateState(dbCtx, req.RequestID, UpdateStateOpts{
			ChildPID:   pid,
			WatchdogAt: &deadline,
		})
		cancel()
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		// Clear the child_pid since the process is reaped. Output
		// captured in `buf` either way.
		clearChildPID(req.RequestID)
		return buf.Bytes(), err
	case <-time.After(timeout):
		log.Printf("watchdog: %s pid=%d deadline=%s exceeded — sending SIGTERM", action, pid, timeout)
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
			clearChildPID(req.RequestID)
			return buf.Bytes(), fmt.Errorf("%w: %s did not complete within %s (terminated cleanly after SIGTERM)", ErrWatchdog, action, timeout)
		case <-time.After(time.Duration(watchdogGraceSeconds) * time.Second):
			log.Printf("watchdog: %s pid=%d still alive after SIGTERM — sending SIGKILL", action, pid)
			_ = cmd.Process.Kill()
			<-done
			clearChildPID(req.RequestID)
			return buf.Bytes(), fmt.Errorf("%w: %s did not complete within %s (SIGKILLed after %ds grace)", ErrWatchdog, action, timeout, watchdogGraceSeconds)
		}
	}
}

func clearChildPID(requestID string) {
	if ops == nil || requestID == "" {
		return
	}
	dbCtx, cancel := context.WithTimeout(context.Background(), opsLookupDeadline)
	defer cancel()
	_ = ops.UpdateState(dbCtx, requestID, UpdateStateOpts{ClearChildPID: true})
}
