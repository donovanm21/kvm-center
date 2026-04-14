package main

import (
	"net/http"
	"strconv"
	"time"
)

// GET /api/audit — paginated, filterable audit-log reader.
// Query params (all optional):
//   actor    actor_id exact match
//   action   action name (canonical; see audit.go constants)
//   resource substring match (case-insensitive)
//   since    unix seconds
//   until    unix seconds
//   success  "true" | "false"
//   offset   default 0
//   limit    default 100, max 500
func handleAuditList(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		q := r.URL.Query()
		filter := AuditFilter{
			ActorID:  q.Get("actor"),
			Action:   q.Get("action"),
			Resource: q.Get("resource"),
			Offset:   parseIntDefault(q.Get("offset"), 0),
			Limit:    parseIntDefault(q.Get("limit"), 100),
		}
		if filter.Limit > 500 {
			filter.Limit = 500
		}
		if s := q.Get("since"); s != "" {
			if ts, err := strconv.ParseInt(s, 10, 64); err == nil {
				filter.Since = time.Unix(ts, 0)
			}
		}
		if s := q.Get("until"); s != "" {
			if ts, err := strconv.ParseInt(s, 10, 64); err == nil {
				filter.Until = time.Unix(ts, 0)
			}
		}
		if s := q.Get("success"); s != "" {
			b := s == "true" || s == "1"
			filter.Success = &b
		}
		events, total := store.GetAuditEvents(filter)
		writeJSON(w, http.StatusOK, map[string]any{
			"events": events,
			"total":  total,
			"offset": filter.Offset,
			"limit":  filter.Limit,
		})
	}
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
