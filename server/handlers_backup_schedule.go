package main

// HTTP handlers for the v5.43.0 backup scheduler. GET returns the
// current schedule (or null when not configured); PUT replaces it.
// Both gated on user.manage at route registration.

import (
	"encoding/json"
	"net/http"
	"strings"

	"kvm-center/internal/models"
)

func handleBackupScheduleGet(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		writeJSON(w, http.StatusOK, store.GetBackupSchedule())
	}
}

func handleBackupSchedulePut(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "PUT or POST only")
			return
		}
		var sc models.BackupSchedule
		if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		// Disabled schedules don't need destination/interval; only
		// validate when the schedule is on.
		if sc.Enabled {
			if sc.EveryHours <= 0 || sc.EveryHours > 24*30 {
				writeError(w, http.StatusBadRequest, "every_hours must be 1..720")
				return
			}
			if strings.TrimSpace(sc.DestPath) == "" {
				writeError(w, http.StatusBadRequest, "dest_path is required when enabled")
				return
			}
			if sc.RetainCount < 0 || sc.RetainCount > 365 {
				writeError(w, http.StatusBadRequest, "retain_count must be 0..365")
				return
			}
		}
		// Preserve outcome timestamps so a config change doesn't
		// erase the operator's "last run was 3 hours ago" view.
		if cur := store.GetBackupSchedule(); cur != nil {
			sc.LastRunAt = cur.LastRunAt
			sc.LastRunErr = cur.LastRunErr
			sc.LastRunSize = cur.LastRunSize
		}
		store.SetBackupSchedule(&sc)
		actor := ""
		if u := userFromRequest(r); u != nil {
			actor = u.ID
		}
		audit(store, r, AuditBackupExport, "backup", map[string]string{
			"action":  "schedule.update",
			"enabled": boolStr(sc.Enabled),
			"actor":   actor,
		}, true, "")
		writeJSON(w, http.StatusOK, store.GetBackupSchedule())
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
