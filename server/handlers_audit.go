package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"kvm-center/internal/models"
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

// auditShippingRouter multiplexes GET/PUT on the same path so the UI
// can do a simple "load then save" without two separate endpoints.
func auditShippingRouter(store *Store) http.HandlerFunc {
	get := handleAuditShippingGet(store)
	put := handleAuditShippingPut(store)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			get(w, r)
		case http.MethodPut, http.MethodPost:
			put(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "GET or PUT")
		}
	}
}

// GET /api/settings/audit-shipping — returns the current config plus
// last-ship metadata so the UI can show a green/red health pill.
// BearerToken / BasicAuth are NEVER returned; the UI shows a
// "(configured)" placeholder so operators know something is set
// without the value leaking to anyone with audit.view.
func handleAuditShippingGet(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := store.auditShippingSnapshot()
		if cfg == nil {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
			return
		}
		// Redact secrets before echoing.
		safe := *cfg
		if safe.BearerToken != "" {
			safe.BearerToken = "(configured)"
		}
		if safe.BasicAuth != "" {
			safe.BasicAuth = "(configured)"
		}
		writeJSON(w, http.StatusOK, safe)
	}
}

// PUT /api/settings/audit-shipping — accepts the full config. Empty
// BearerToken / BasicAuth in the payload are treated as "leave the
// existing value alone" so the UI's redaction dance doesn't
// accidentally blank a secret on every save.
func handleAuditShippingPut(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "PUT/POST only")
			return
		}
		var cfg struct {
			Enabled        bool   `json:"enabled"`
			Type           string `json:"type"`
			Endpoint       string `json:"endpoint"`
			BearerToken    string `json:"bearer_token"`
			BasicAuth      string `json:"basic_auth"`
			SyslogFacility int    `json:"syslog_facility"`
			SyslogAppName  string `json:"syslog_app_name"`
			BatchSize      int    `json:"batch_size"`
			FlushSeconds   int    `json:"flush_seconds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if cfg.Enabled && cfg.Endpoint == "" {
			writeError(w, http.StatusBadRequest, "endpoint required when enabled")
			return
		}
		if cfg.Enabled && cfg.Type != "syslog" && cfg.Type != "http" {
			writeError(w, http.StatusBadRequest, "type must be 'syslog' or 'http'")
			return
		}
		// Preserve previously-stored secrets when the caller sends
		// empty strings (UI redacts them to the placeholder, so a
		// naive save would otherwise wipe them).
		prev := store.auditShippingSnapshot()
		final := &models.AuditShipping{
			Enabled:        cfg.Enabled,
			Type:           cfg.Type,
			Endpoint:       cfg.Endpoint,
			BearerToken:    cfg.BearerToken,
			BasicAuth:      cfg.BasicAuth,
			SyslogFacility: cfg.SyslogFacility,
			SyslogAppName:  cfg.SyslogAppName,
			BatchSize:      cfg.BatchSize,
			FlushSeconds:   cfg.FlushSeconds,
		}
		if prev != nil {
			if final.BearerToken == "" || final.BearerToken == "(configured)" {
				final.BearerToken = prev.BearerToken
			}
			if final.BasicAuth == "" || final.BasicAuth == "(configured)" {
				final.BasicAuth = prev.BasicAuth
			}
		}
		store.SetAuditShipping(final)
		audit(store, r, AuditAuditShipping, "audit-shipping",
			map[string]string{"enabled": boolToStr(cfg.Enabled), "type": cfg.Type, "endpoint": cfg.Endpoint}, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// POST /api/settings/audit-shipping/test — ships a single synthetic
// event to the configured endpoint so the operator can sanity-check
// the wiring without waiting for the next audit event.
func handleAuditShippingTest(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := store.auditShippingSnapshot()
		if cfg == nil || cfg.Endpoint == "" {
			writeError(w, http.StatusBadRequest, "audit shipping is not configured")
			return
		}
		ev := &models.AuditEvent{
			ID:        newAuditID(),
			At:        time.Now().UTC(),
			ActorName: "audit-shipping-test",
			Action:    "audit.shipping.test",
			Resource:  "audit-shipping",
			Success:   true,
			Detail:    map[string]string{"note": "test ping from kvm-center"},
		}
		if err := shipAuditBatch(cfg, []*models.AuditEvent{ev}); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
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
