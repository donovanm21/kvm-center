package main

// Object-level concurrency control (v5.48.1 PR-3c).
//
// Refuses a second mutating /api/command call against the same VM
// while another is in flight. Returns 409 Conflict with the
// conflicting request_id so the SPA can disable the button + link
// to the in-flight task in Recent Tasks.
//
// Conflict matrix is permissive on purpose: any pending row on
// the same target counts. Pre-v5.48 we had no per-target locking
// at all and the failure mode was "operator clicks STOP twice;
// libvirtd refuses the second; SPA shows confusing error". With
// 409 the SPA shows a clear "an action is already in flight"
// message + the conflicting task's progress.

import (
	"strings"

	"kvm-center/internal/models"
)

// findInflightConflict scans the command log for a pending entry
// targeting the same VM. Returns nil if none, or the conflicting
// entry to surface in the 409 response.
//
// The current request's own request_id is excluded so an
// idempotent retry isn't flagged as a self-conflict.
func findInflightConflict(log *CommandLog, action, vmUUID, vmName, ownRequestID string) *models.CommandLogEntry {
	if log == nil {
		return nil
	}
	if vmUUID == "" && vmName == "" {
		return nil
	}
	uuidLower := strings.ToLower(vmUUID)
	for _, e := range log.List(0) {
		if e.RequestID == ownRequestID {
			continue
		}
		if e.Outcome != "pending" {
			continue
		}
		if !sameTarget(e, uuidLower, vmName) {
			continue
		}
		_ = action // future: per-action conflict matrix; today any pending wins
		return e
	}
	return nil
}

func sameTarget(e *models.CommandLogEntry, uuidLower, vmName string) bool {
	if uuidLower != "" && strings.ToLower(e.VMUUID) == uuidLower {
		return true
	}
	if vmName != "" && e.VMName == vmName {
		return true
	}
	return false
}
