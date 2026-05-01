package main

// Tasks API (v5.48.1 PR-3b).
//
// vSphere-parity Recent Tasks. The SPA's bottom-of-screen pane
// reads from these endpoints + subscribes to SSE `command` and
// `command_progress` events for live updates.
//
// Three endpoints:
//   GET /api/tasks/recent?limit=N     — newest-first list
//   GET /api/tasks?since=<unix>       — incremental; for SPA reconnect
//   GET /api/tasks/<request_id>       — single-row detail
//
// RBAC: admins (`user.manage`) see everything. Non-admins see
// rows where ActorID matches their user ID. This mirrors the
// existing /api/admin/command-log pattern but extends visibility
// to self-initiated rows so an Operator can watch their own
// migration without an admin on shift.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// handleTasksRecent: GET /api/tasks/recent?limit=20
func handleTasksRecent(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		limit := 20
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		entries := visibleTasks(store, r, 0)
		if len(entries) > limit {
			entries = entries[:limit]
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"entries": entries,
			"count":   len(entries),
		})
	}
}

// handleTasksSince: GET /api/tasks?since=<unix>. Returns rows
// where SubmittedAt OR AppliedAt is at or after the given second
// boundary. Used by the SPA on tab-focus / reconnect to backfill
// anything that completed while the tab was hidden.
func handleTasksSince(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		since := time.Time{}
		if v := r.URL.Query().Get("since"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				since = time.Unix(n, 0)
			}
		}
		all := visibleTasks(store, r, 0)
		var out []*models.CommandLogEntry
		for _, e := range all {
			if since.IsZero() {
				out = append(out, e)
				continue
			}
			if e.SubmittedAt.After(since) || (e.AppliedAt != nil && e.AppliedAt.After(since)) {
				out = append(out, e)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"entries": out,
			"count":   len(out),
			"now":     time.Now().Unix(),
		})
	}
}

// handleTaskByID: GET /api/tasks/<request_id>
func handleTaskByID(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
		id = strings.TrimSuffix(id, "/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusBadRequest, "invalid request_id")
			return
		}
		u := userFromRequest(r)
		isAdmin := u != nil && hasPermission(store, u, models.PermUserManage)
		for _, e := range store.commandLog.List(0) {
			if e.RequestID != id {
				continue
			}
			if !isAdmin && (u == nil || e.ActorID != u.ID) {
				writeError(w, http.StatusForbidden, "not authorised to view this task")
				return
			}
			writeJSON(w, http.StatusOK, e)
			return
		}
		writeError(w, http.StatusNotFound, "task not found")
	}
}

// visibleTasks returns command_log entries the caller is allowed
// to see, newest-first. limit=0 means no cap (caller slices).
func visibleTasks(store *Store, r *http.Request, limit int) []*models.CommandLogEntry {
	all := store.commandLog.List(limit)
	u := userFromRequest(r)
	if u == nil {
		return nil
	}
	if hasPermission(store, u, models.PermUserManage) {
		return all
	}
	out := all[:0]
	for _, e := range all {
		if e.ActorID == u.ID {
			out = append(out, e)
		}
	}
	return out
}
