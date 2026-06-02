//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

// spawnRestartHelper launches a one-shot helper that waits a few
// seconds then runs `schtasks /Run /TN kvm-tools`. The helper has
// to outlive its parent (kvm-tools is about to os.Exit, and Task
// Scheduler tears down the entire Job object when its action
// exits). CREATE_BREAKAWAY_FROM_JOB lets the child escape the
// parent's Job; DETACHED_PROCESS gives it no console handle to
// inherit; CREATE_NEW_PROCESS_GROUP isolates Ctrl-C/Ctrl-Break.
//
// The helper writes its progress to logPath so a follow-up
// session can verify the relaunch sequence end-to-end.
func spawnRestartHelper(logPath string) error {
	const (
		detachedProcess        = 0x00000008
		createNewProcessGroup  = 0x00000200
		createBreakawayFromJob = 0x01000000
	)

	helperCmd := fmt.Sprintf(
		`(echo [%%DATE%% %%TIME%%] kvm-tools restart helper firing & timeout /t 5 /nobreak ^>nul & schtasks /Run /TN kvm-tools) ^>^> "%s" 2^>^&1`,
		logPath,
	)

	cmd := exec.Command("cmd", "/c", helperCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: detachedProcess | createNewProcessGroup | createBreakawayFromJob,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn helper: %w", err)
	}
	// Detach: don't wait. The helper has its own stdio (the >> redirect
	// in the cmd line) and its own process group.
	_ = cmd.Process.Release()
	return nil
}
