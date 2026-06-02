package main

// HTTP surface for the Discord Gateway integration (v5.52.0).
//
//   GET  /api/settings/discord-gateway          → status + config (token redacted)
//   PUT  /api/settings/discord-gateway          → save config; if Enabled, kicks off /pair/request
//   POST /api/settings/discord-gateway/pair     → re-pair (clears creds + retries handshake)
//   POST /api/settings/discord-gateway/revoke   → wipe local pairing creds (operator-side revoke)
//   POST /api/settings/discord-gateway/test     → fire one synthetic card through the live wiring

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// discordGatewayRouter dispatches GET vs PUT on the same path.
func discordGatewayRouter(store *Store) http.HandlerFunc {
	get := handleDiscordGatewayGet(store)
	put := handleDiscordGatewayPut(store)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			get(w, r)
		case http.MethodPut, http.MethodPost:
			put(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "GET / PUT only")
		}
	}
}

func handleDiscordGatewayGet(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := store.discordGatewayPublicSnapshot()
		if cfg == nil {
			cfg = &models.DiscordGateway{Status: dgStatusDisabled, TLS: true}
		}
		writeJSON(w, http.StatusOK, cfg)
	}
}

// handleDiscordGatewayPut saves the form. If Enabled flips on (or
// host/port changes), schedule a fresh /pair/request — the singleton's
// status moves to "pending" and the worker poller takes over.
func handleDiscordGatewayPut(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Enabled             bool   `json:"enabled"`
			Host                string `json:"host"`
			Port                int    `json:"port"`
			TLS                 bool   `json:"tls"`
			VerifyTLS           bool   `json:"verify_tls"`
			ShipAuditEvents     bool   `json:"ship_audit_events"`
			ShipAuditFailedOnly bool   `json:"ship_audit_failed_only"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		req.Host = strings.TrimSpace(req.Host)
		if req.Enabled && (req.Host == "" || req.Port == 0) {
			writeError(w, http.StatusBadRequest, "host + port required when enabled")
			return
		}

		prev := store.discordGatewaySnapshot()
		// Detect the bits that require a fresh pair: enabled flipping
		// on, or endpoint changing. Internal state (registration_id /
		// token) is retained when the operator just toggles VerifyTLS
		// or saves the same form again.
		needsPair := req.Enabled && (prev == nil ||
			!prev.Enabled || prev.Host != req.Host || prev.Port != req.Port || prev.TLS != req.TLS)

		merged := &models.DiscordGateway{
			Enabled:             req.Enabled,
			Host:                req.Host,
			Port:                req.Port,
			TLS:                 req.TLS,
			VerifyTLS:           req.VerifyTLS,
			ShipAuditEvents:     req.ShipAuditEvents,
			ShipAuditFailedOnly: req.ShipAuditFailedOnly,
		}
		if !req.Enabled {
			merged.Status = dgStatusDisabled
		} else if needsPair {
			merged.Status = dgStatusSaving
		} else if prev != nil {
			// Preserve pairing state across cosmetic edits.
			merged.RegistrationID = prev.RegistrationID
			merged.WebhookID = prev.WebhookID
			merged.Token = prev.Token
			merged.Status = prev.Status
			merged.PendingSince = prev.PendingSince
			merged.LastHealthAt = prev.LastHealthAt
			merged.LastHealthError = prev.LastHealthError
			merged.LastSendAt = prev.LastSendAt
			merged.LastSendError = prev.LastSendError
			merged.LastSendCount = prev.LastSendCount
			merged.DroppedEvents = prev.DroppedEvents
		}
		store.SetDiscordGateway(merged)

		if needsPair {
			go runPairRequest(store, r)
		}

		audit(store, r, AuditDiscordGwUpdate, "discord-gateway", map[string]string{
			"enabled":    boolToStr(req.Enabled),
			"host":       req.Host,
			"verify_tls": boolToStr(req.VerifyTLS),
		}, true, "")

		writeJSON(w, http.StatusOK, store.discordGatewayPublicSnapshot())
	}
}

// handleDiscordGatewayPair forces a re-pair: clears creds + state,
// runs /pair/request again. Useful when the operator deleted the
// registration on the gateway side and needs a clean slate.
//
// Optionally accepts the same payload as PUT — the body is merged
// onto the persisted config before pairing. This lets the UI ship a
// single "Save form changes + Re-pair" click without two separate
// requests, and means an operator who toggles VerifyTLS in the
// panel and immediately hits Re-pair gets the new TLS posture
// instead of the previous saved value.
func handleDiscordGatewayPair(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		// Best-effort body parse — older clients send {} or no body.
		var req struct {
			Enabled   *bool   `json:"enabled,omitempty"`
			Host      *string `json:"host,omitempty"`
			Port      *int    `json:"port,omitempty"`
			TLS       *bool   `json:"tls,omitempty"`
			VerifyTLS *bool   `json:"verify_tls,omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req) // ignore EOF / empty body

		cfg := store.discordGatewaySnapshot()
		if cfg == nil || !cfg.Enabled {
			// Allow re-pair to also flip on enabled if the body says so —
			// matches the PUT shape so the UI has one mental model.
			if req.Enabled == nil || !*req.Enabled {
				writeError(w, http.StatusBadRequest, "discord-gateway not enabled")
				return
			}
		}
		store.patchDiscordGateway(func(g *models.DiscordGateway) {
			if req.Enabled != nil {
				g.Enabled = *req.Enabled
			}
			if req.Host != nil {
				g.Host = strings.TrimSpace(*req.Host)
			}
			if req.Port != nil {
				g.Port = *req.Port
			}
			if req.TLS != nil {
				g.TLS = *req.TLS
			}
			if req.VerifyTLS != nil {
				g.VerifyTLS = *req.VerifyTLS
			}
			g.RegistrationID = ""
			g.WebhookID = ""
			g.Token = ""
			g.Status = dgStatusSaving
			g.PendingSince = ""
			g.LastHealthError = ""
		})
		go runPairRequest(store, r)
		audit(store, r, AuditDiscordGwPair, "discord-gateway", nil, true, "")
		writeJSON(w, http.StatusOK, store.discordGatewayPublicSnapshot())
	}
}

// handleDiscordGatewayRevoke clears local creds + drops to disabled.
// The operator must also remove the pairing on the gateway's side
// for a complete revoke; this just stops kvm-center from using it.
func handleDiscordGatewayRevoke(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		store.SetDiscordGateway(&models.DiscordGateway{Status: dgStatusDisabled, TLS: true})
		audit(store, r, AuditDiscordGwRevoke, "discord-gateway", nil, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// handleDiscordGatewayTest fires a synthetic card through the live
// wiring so the operator can prove the path before waiting for a
// real notification to arrive.
func handleDiscordGatewayTest(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		cfg := store.discordGatewaySnapshot()
		if cfg == nil || cfg.Status != dgStatusHealthy {
			writeError(w, http.StatusBadRequest, "gateway is not healthy — pair + reach the gateway first")
			return
		}
		c := newGatewayClient(store)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := c.sendTestCard(ctx); err != nil {
			audit(store, r, AuditDiscordGwTest, "discord-gateway", nil, false, err.Error())
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		audit(store, r, AuditDiscordGwTest, "discord-gateway", nil, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// runPairRequest fires /pair/request asynchronously so the PUT
// handler returns to the UI fast (otherwise a slow gateway would
// block the operator's form submit for the full HTTP timeout).
func runPairRequest(store *Store, r *http.Request) {
	c := newGatewayClient(store)
	cfg := store.discordGatewaySnapshot()
	if cfg == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	source := map[string]string{
		"source_kind": "kvm-center",
		"source_name": deriveDeploymentName(),
		"source_url":  store.GetFeatures().PublicURL,
		"version":     Version,
		"deployment":  deriveDeploymentName(),
	}
	_ = c.pairRequest(ctx, source)
}

// deriveDeploymentName returns the operator-set deployment name
// from the env (kvm-tools / discord bot share the same convention)
// falling back to a generic kvm-center label.
func deriveDeploymentName() string {
	if n := strings.TrimSpace(envOrDefault("KVMC_DEPLOYMENT_NAME", "")); n != "" {
		return n
	}
	return "kvm-center"
}
