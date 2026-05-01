package main

// Task-outcome notifications (v5.48.1 PR-3d).
//
// Hooks the existing notification subsystem (channels +
// dispatcher in notifications.go) into command-log terminal
// transitions:
//   - failures / cancelled / unknown → fire on every transition
//     (gated by Features.NotifyOnTaskFailure, default true).
//   - successes → opt-in per-operator via
//     User.NotifyTaskSuccessActions.
//
// Called from the three places a command_log entry transitions:
//   - handleCommand's success / failed / cancelled branches
//     (sync path),
//   - command_reconciler.reconcileOne (async outcome poll),
//   - handlers_command_cancel.handleCommandCancel (operator
//     cancel proxy).

import (
	"strings"
	"time"

	"kvm-center/internal/models"
)

// notifyTaskOutcome fires a notification for a terminal command-
// log transition. Best-effort: logs nothing on success path,
// silently no-ops on misconfiguration.
//
// outcome must be one of "success" / "failed" / "cancelled" /
// "unknown". Anything else is ignored.
func notifyTaskOutcome(store *Store, e *models.CommandLogEntry, outcome, reason string) {
	if store == nil || e == nil {
		return
	}

	switch outcome {
	case "failed", "cancelled", "unknown":
		feats := store.GetFeatures()
		if !feats.NotifyOnTaskFailure {
			return
		}
		fireTaskNotification(store, e, outcome, reason)
	case "success":
		if shouldNotifySuccess(store, e) {
			fireTaskNotification(store, e, outcome, reason)
		}
	}
}

func shouldNotifySuccess(store *Store, e *models.CommandLogEntry) bool {
	if e.ActorID == "" {
		return false
	}
	u := store.GetUserByID(e.ActorID)
	if u == nil || len(u.NotifyTaskSuccessActions) == 0 {
		return false
	}
	want := strings.ToUpper(e.Action)
	for _, a := range u.NotifyTaskSuccessActions {
		if strings.ToUpper(a) == want {
			return true
		}
	}
	return false
}

func fireTaskNotification(store *Store, e *models.CommandLogEntry, outcome, reason string) {
	severity := "info"
	switch outcome {
	case "failed":
		severity = "critical"
	case "cancelled", "unknown":
		severity = "warning"
	}
	target := e.VMName
	if target == "" {
		target = e.HostID
	}
	title := "Task " + outcome + ": " + e.Action + " on " + target
	msg := reason
	if msg == "" {
		msg = e.Message
	}
	store.fireNotification(&models.NotificationEvent{
		Type:     "command." + outcome,
		Title:    title,
		Message:  msg,
		Resource: "vm/" + e.VMUUID,
		Severity: severity,
		At:       time.Now(),
		Detail: map[string]string{
			"request_id": e.RequestID,
			"action":     e.Action,
			"host_id":    e.HostID,
			"vm_uuid":    e.VMUUID,
			"vm_name":    e.VMName,
			"actor_id":   e.ActorID,
			"outcome":    outcome,
		},
	})
}
