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

// scheduleResponse is what GET returns — same shape as
// `BackupSchedule` minus the passphrase (never echoed) plus a
// `has_passphrase` flag so the UI can show "configured" status.
type scheduleResponse struct {
	*models.BackupSchedule
	HasPassphrase bool `json:"has_passphrase"`
}

func handleBackupScheduleGet(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		sc := store.GetBackupSchedule()
		if sc == nil {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		// Strip the secret half — never echo the passphrase.
		strip := *sc
		strip.EncryptionPassphrase = ""
		writeJSON(w, http.StatusOK, scheduleResponse{
			BackupSchedule: &strip,
			HasPassphrase:  sc.EncryptionPassphrase != "",
		})
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
			cron := strings.TrimSpace(sc.CronExpr)
			if cron != "" {
				if _, err := cronParser.Parse(cron); err != nil {
					writeError(w, http.StatusBadRequest, "cron_expr: "+err.Error())
					return
				}
			} else if sc.EveryHours <= 0 || sc.EveryHours > 24*30 {
				writeError(w, http.StatusBadRequest, "set either cron_expr or every_hours (1..720)")
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
		// Treat empty `encryption_passphrase` as "keep current" so a
		// PATCH-style UI roundtrip doesn't accidentally drop it.
		if cur := store.GetBackupSchedule(); cur != nil {
			sc.LastRunAt = cur.LastRunAt
			sc.LastRunErr = cur.LastRunErr
			sc.LastRunSize = cur.LastRunSize
			if sc.EncryptionPassphrase == "" {
				sc.EncryptionPassphrase = cur.EncryptionPassphrase
			}
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
