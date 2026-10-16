package main

// Discord Gateway client (v5.52.0).
//
// Three goroutines own the lifecycle of a paired Discord-Gateway:
//   - pairingPoller — runs while status==pending; calls /pair/status
//     until the operator approves the pairing in the gateway's UI.
//   - healthMonitor — runs while status ∈ {approved,connecting,healthy};
//     calls /healthz on a 30 s tick to surface gateway liveness +
//     Discord-side websocket readiness.
//   - sendWorker    — drains store.discordGwQueue while status==healthy;
//     each notification event becomes one card POSTed to the gateway's
//     /webhook/<id> endpoint with the bearer token.
//
// Token is stored plaintext (operator-validated tradeoff; matches the
// SMTPPassword / MetricsConfig.Token pattern). API responses redact
// the value to "(configured)".

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// Status values surfaced to the UI badge. Must match the strings the
// frontend's NotificationsTab key off — keep in sync.
const (
	dgStatusDisabled    = "disabled"
	dgStatusSaving      = "saving"
	dgStatusPending     = "pending"
	dgStatusApproved    = "approved"
	dgStatusConnecting  = "connecting"
	dgStatusHealthy     = "healthy"
	dgStatusUnreachable = "unreachable"
	dgStatusRevoked     = "revoked"
	dgStatusTimeout     = "timeout"
)

const (
	dgPairingApprovalDeadline = 1 * time.Hour
	dgHealthInterval          = 30 * time.Second
	dgHealthFailThreshold     = 2
	dgVerifyTLSWarnInterval   = 6 * time.Hour
	dgSendQueueCap            = 1024

	// Pairing poll cadence — fast for the first 2 min while the
	// operator is likely actively approving, then back off so we
	// don't beat up the gateway for an hour.
	dgPairingFastPoll = 5 * time.Second
	dgPairingSlowPoll = 30 * time.Second
	dgPairingFastFor  = 2 * time.Minute

	// dgPairSavingFloor (v5.60.0) — minimum time the singleton can sit
	// in `saving` without a registration_id before sendCard's
	// stale-state guard reclassifies it as broken. Set well past the
	// normal pairRequest round-trip (sub-second on a healthy gateway)
	// so a brief network blip during pair doesn't immediately flip
	// the state to revoked.
	dgPairSavingFloor = 2 * time.Minute
)

// isStaleSince returns true when `tsRFC3339` is either empty (never
// set) or older than `floor`. Used by the v5.60.0 stuck-state guard
// in sendCard to decide whether a non-terminal status (saving /
// pending) has lingered long enough to call broken.
func isStaleSince(tsRFC3339 string, floor, fallbackWhenEmpty time.Duration) bool {
	if strings.TrimSpace(tsRFC3339) == "" {
		// Empty timestamp shouldn't normally co-occur with these
		// states, but if it does the safest assumption is "stuck"
		// (no way to know when it entered the state).
		return fallbackWhenEmpty > 0
	}
	t, err := time.Parse(time.RFC3339, tsRFC3339)
	if err != nil {
		return true
	}
	return time.Since(t) > floor
}

// dgCard is the on-the-wire payload shape POSTed to the gateway's
// /webhook/<id> endpoint. Mirrors the spec §4.2 — the gateway's
// generic adapter renders it into a Discord embed.
type dgCard struct {
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Severity    string    `json:"severity,omitempty"`
	Type        string    `json:"type,omitempty"`
	Resource    string    `json:"resource,omitempty"`
	Fields      []dgField `json:"fields,omitempty"`
}

type dgField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// gatewayClient is the small HTTP client used by all three workers.
// Stateless — every call reads s.discordGateway under the lock so a
// config change picks up immediately without restarting the worker.
type gatewayClient struct {
	store *Store
}

// newGatewayClient returns a client bound to the store.
func newGatewayClient(s *Store) *gatewayClient {
	return &gatewayClient{store: s}
}

// snapshot returns a defensive copy under the read lock so workers
// don't race the operator's PUT.
func (s *Store) discordGatewaySnapshot() *models.DiscordGateway {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.discordGateway == nil {
		return nil
	}
	cp := *s.discordGateway
	return &cp
}

// SetDiscordGateway replaces the singleton config; persists. Pass nil
// to clear (operator-side revoke).
func (s *Store) SetDiscordGateway(cfg *models.DiscordGateway) {
	s.mu.Lock()
	s.discordGateway = cfg
	s.mu.Unlock()
	s.AsyncSave()
}

// patchDiscordGateway runs `mut` under the write lock against the
// live pointer (creating it if nil). Used by the workers to update
// state-machine + observability fields without racing other writers.
func (s *Store) patchDiscordGateway(mut func(*models.DiscordGateway)) {
	s.mu.Lock()
	if s.discordGateway == nil {
		s.discordGateway = &models.DiscordGateway{}
	}
	mut(s.discordGateway)
	s.mu.Unlock()
	s.AsyncSave()
}

// dgEndpoint composes the base URL from the persisted config. Returns
// "" if not enough fields are set to talk to the gateway. Free fn
// rather than a method because models is a sibling package — we
// can't hang receivers on types we don't own.
func dgEndpoint(cfg *models.DiscordGateway) string {
	if cfg == nil || cfg.Host == "" || cfg.Port == 0 {
		return ""
	}
	scheme := "https"
	if !cfg.TLS {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, cfg.Host, cfg.Port)
}

// httpClient returns a *http.Client honouring VerifyTLS. Operators
// running the stock gateway compose hit a self-signed cert, so the
// default is verify=false (with a periodic warn-log nag).
func (c *gatewayClient) httpClient(cfg *models.DiscordGateway) *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: !cfg.VerifyTLS, //nolint:gosec // operator-opt-in for self-signed gateway
		},
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: tr}
}

// ── pairing handshake ────────────────────────────────────────────

// pairRequest hits POST /pair/request and stores the registration
// ID. Caller must have previously validated the host/port + flipped
// status to "saving".
func (c *gatewayClient) pairRequest(ctx context.Context, source map[string]string) error {
	cfg := c.store.discordGatewaySnapshot()
	if cfg == nil {
		return fmt.Errorf("discord-gateway: not configured")
	}
	url := dgEndpoint(cfg) + "/pair/request"
	body, _ := json.Marshal(source)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient(cfg).Do(req)
	if err != nil {
		c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
			g.Status = dgStatusUnreachable
			g.LastHealthError = err.Error()
		})
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return fmt.Errorf("pair: unexpected %s — %s", resp.Status, strings.TrimSpace(string(buf)))
	}
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("pair: decode: %w", err)
	}
	c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
		g.RegistrationID = out.ID
		g.Status = dgStatusPending
		g.PendingSince = time.Now().UTC().Format(time.RFC3339)
	})
	return nil
}

// pollStatus hits GET /pair/status/<id>. Captures the token on the
// first "approved" reply, returns the new status string.
func (c *gatewayClient) pollStatus(ctx context.Context) (string, error) {
	cfg := c.store.discordGatewaySnapshot()
	if cfg == nil || cfg.RegistrationID == "" {
		return "", fmt.Errorf("discord-gateway: no registration")
	}
	url := dgEndpoint(cfg) + "/pair/status/" + cfg.RegistrationID
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.httpClient(cfg).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Registration expired — treat as a soft revoke; operator
		// must re-pair.
		c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
			g.Status = dgStatusRevoked
		})
		return dgStatusRevoked, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pair-status: %s", resp.Status)
	}
	var out struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		WebhookID string `json:"webhook_id"`
		Token     string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("pair-status: decode: %w", err)
	}
	switch out.Status {
	case dgStatusApproved:
		c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
			g.Status = dgStatusApproved
			if out.WebhookID != "" {
				g.WebhookID = out.WebhookID
			}
			if out.Token != "" {
				g.Token = out.Token
			}
		})
		return dgStatusApproved, nil
	case dgStatusRevoked:
		c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
			g.Status = dgStatusRevoked
		})
		return dgStatusRevoked, nil
	default:
		return out.Status, nil
	}
}

// healthz hits GET /healthz, returns the parsed reply. Sets
// LastHealthAt + LastHealthError as a side-effect.
func (c *gatewayClient) healthz(ctx context.Context) (ok, ready bool, err error) {
	cfg := c.store.discordGatewaySnapshot()
	if cfg == nil {
		return false, false, fmt.Errorf("discord-gateway: not configured")
	}
	url := dgEndpoint(cfg) + "/healthz"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.httpClient(cfg).Do(req)
	now := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
			g.LastHealthAt = now
			g.LastHealthError = err.Error()
		})
		return false, false, err
	}
	defer resp.Body.Close()
	var out struct {
		OK            bool `json:"ok"`
		DiscordReady  bool `json:"discord_ready"`
		UptimeSeconds int  `json:"uptime_seconds"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
		g.LastHealthAt = now
		g.LastHealthError = ""
	})
	return out.OK, out.DiscordReady, nil
}

// sendCard ships one card. Returns true if it landed (204 / 200).
// 401/404 flips the singleton to "revoked"; other transient errors
// return false + log so the caller can retry / drop.
func (c *gatewayClient) sendCard(ctx context.Context, card dgCard) (bool, error) {
	cfg := c.store.discordGatewaySnapshot()
	if cfg == nil || cfg.WebhookID == "" || cfg.Token == "" {
		// v5.53.1: if status currently claims healthy/approved/connecting
		// but the token is gone, the panel will keep saying "Healthy"
		// while every send silently fails. Auto-flip to revoked so the
		// operator sees the "Re-pair" prompt instead of a misleading
		// green badge.
		//
		// v5.60.0: extended to also cover stuck `saving` (and `pending`
		// past the operator-approval deadline). The pair flow normally
		// transitions saving→pending within seconds and pending→approved
		// within the 1h approval window; if the goroutine that drives
		// either transition dies (server crash, network blip), the
		// singleton lingers in the non-terminal state forever and every
		// audit event silently increments dropped_events. Flip to
		// revoked once we observe the symptom (a send attempt with no
		// token) AND the state hasn't moved in the expected window.
		if cfg != nil && cfg.Enabled {
			stuck := false
			switch cfg.Status {
			case dgStatusHealthy, dgStatusApproved, dgStatusConnecting:
				stuck = true
			case dgStatusSaving:
				// Saving without a token + no registration_id means the
				// pair-request goroutine never landed. The handler that
				// set saving spawns runPairRequest immediately; >2 min
				// in saving with empty reg_id == stuck.
				if cfg.RegistrationID == "" {
					stuck = isStaleSince(cfg.PendingSince, 2*time.Minute, dgPairSavingFloor)
				}
			case dgStatusPending:
				// Pending past the approval deadline = operator never
				// approved (or never will). Flip so the panel surfaces
				// the Re-pair button instead of staying yellow forever.
				stuck = isStaleSince(cfg.PendingSince, dgPairingApprovalDeadline, dgPairingApprovalDeadline)
			}
			if stuck {
				c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
					g.Status = dgStatusRevoked
					g.LastSendError = "no bearer token on file — re-pair required"
				})
			}
		}
		return false, fmt.Errorf("discord-gateway: not paired")
	}
	url := dgEndpoint(cfg) + "/webhook/" + cfg.WebhookID
	body, _ := json.Marshal(card)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient(cfg).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
			g.LastSendAt = time.Now().UTC().Format(time.RFC3339)
			g.LastSendError = ""
			g.LastSendCount++
		})
		return true, nil
	case http.StatusUnauthorized, http.StatusNotFound:
		c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
			g.Status = dgStatusRevoked
			g.LastSendError = "gateway returned " + resp.Status + " — re-pair required"
		})
		return false, fmt.Errorf("discord-gateway: %s", resp.Status)
	case http.StatusFailedDependency:
		// Gateway has no audit channel set. Surface in
		// LastSendError so the UI can hint Bootstrap.
		c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
			g.LastSendError = "gateway has no audit channel — run Bootstrap"
		})
		return false, fmt.Errorf("discord-gateway: 424 — gateway needs audit channel")
	default:
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
			g.LastSendError = resp.Status + ": " + strings.TrimSpace(string(buf))
		})
		return false, fmt.Errorf("discord-gateway: %s", resp.Status)
	}
}

// ── workers ──────────────────────────────────────────────────────

// startDiscordGatewayWorkers wires the three goroutines. Idempotent
// — the queue init guard means a duplicate call from a misconfigured
// boot path won't double-allocate.
func (s *Store) startDiscordGatewayWorkers(ctx context.Context) {
	s.mu.Lock()
	if s.discordGwQueue == nil {
		s.discordGwQueue = make(chan *models.NotificationEvent, dgSendQueueCap)
	}
	s.mu.Unlock()
	c := newGatewayClient(s)
	s.goWork("pairingPoller", func() { pairingPoller(ctx, c) })
	s.goWork("healthMonitor", func() { healthMonitor(ctx, c) })
	s.goWork("sendWorker", func() { sendWorker(ctx, c) })
}

// pairingPoller advances Pending → Approved (or Timeout). Sleeps when
// status is anything else. Re-checks the singleton each tick so a
// freshly-saved config picks up without a process restart.
func pairingPoller(ctx context.Context, c *gatewayClient) {
	t := time.NewTicker(dgPairingFastPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cfg := c.store.discordGatewaySnapshot()
		if cfg == nil || !cfg.Enabled || cfg.Status != dgStatusPending {
			t.Reset(dgPairingFastPoll)
			continue
		}
		// 1-hour approval window. Beyond that, mark timeout so the
		// UI prompts the operator to retry instead of polling forever.
		since, _ := time.Parse(time.RFC3339, cfg.PendingSince)
		if !since.IsZero() && time.Since(since) > dgPairingApprovalDeadline {
			c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
				g.Status = dgStatusTimeout
			})
			continue
		}
		// Slow down once the fast window has elapsed.
		if !since.IsZero() && time.Since(since) > dgPairingFastFor {
			t.Reset(dgPairingSlowPoll)
		}
		pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := c.pollStatus(pollCtx)
		cancel()
		if err != nil {
			log.Printf("discord-gateway: pair-status: %v", err)
		}
	}
}

// healthMonitor advances Approved/Connecting/Healthy/Unreachable
// based on /healthz responses.
func healthMonitor(ctx context.Context, c *gatewayClient) {
	t := time.NewTicker(dgHealthInterval)
	defer t.Stop()
	consecutiveFails := 0
	lastVerifyTLSWarn := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cfg := c.store.discordGatewaySnapshot()
		if cfg == nil || !cfg.Enabled {
			continue
		}
		// Periodic warn-log when verify_tls is off — keeps the
		// "I forgot I disabled this" risk visible without spamming.
		if !cfg.VerifyTLS && time.Since(lastVerifyTLSWarn) > dgVerifyTLSWarnInterval {
			log.Printf("discord-gateway: verify_tls is disabled — gateway cert is not validated. "+
				"Acceptable on a private LAN; not for public-facing gateways. (host=%s)", cfg.Host)
			lastVerifyTLSWarn = time.Now()
		}
		switch cfg.Status {
		case dgStatusApproved, dgStatusHealthy, dgStatusConnecting, dgStatusUnreachable:
			// fall through
		default:
			continue
		}
		hzCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ok, ready, err := c.healthz(hzCtx)
		cancel()
		if err != nil {
			consecutiveFails++
			if consecutiveFails >= dgHealthFailThreshold {
				c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
					g.Status = dgStatusUnreachable
				})
			}
			continue
		}
		consecutiveFails = 0
		switch {
		case ok && ready:
			c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
				g.Status = dgStatusHealthy
			})
		default:
			c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
				g.Status = dgStatusConnecting
			})
		}
	}
}

// sendWorker drains discordGwQueue. Only ships when status==healthy;
// non-healthy events fall on the floor with DroppedEvents++ so the
// UI can surface "you're losing notifications".
func sendWorker(ctx context.Context, c *gatewayClient) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-c.store.discordGwQueue:
			cfg := c.store.discordGatewaySnapshot()
			if cfg == nil || !cfg.Enabled || cfg.Status != dgStatusHealthy {
				c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
					g.DroppedEvents++
				})
				continue
			}
			card := eventToCard(ev)
			delivered := false
			for _, backoff := range []time.Duration{0, 1 * time.Second, 5 * time.Second, 30 * time.Second} {
				if backoff > 0 {
					select {
					case <-ctx.Done():
						return
					case <-time.After(backoff):
					}
				}
				sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				ok, err := c.sendCard(sendCtx, card)
				cancel()
				if ok {
					delivered = true
					break
				}
				// 401/404 already flipped to revoked — no point retrying.
				if cfg2 := c.store.discordGatewaySnapshot(); cfg2 != nil && cfg2.Status == dgStatusRevoked {
					break
				}
				if err != nil {
					log.Printf("discord-gateway: send retry: %v", err)
				}
			}
			if !delivered {
				c.store.patchDiscordGateway(func(g *models.DiscordGateway) {
					g.DroppedEvents++
				})
			}
		}
	}
}

// eventToCard maps an internal NotificationEvent to the gateway's
// payload shape. Severity remap follows the doc's §4.3 table.
func eventToCard(ev *models.NotificationEvent) dgCard {
	c := dgCard{
		Title:       ev.Title,
		Description: ev.Message,
		Type:        ev.Type,
		Resource:    ev.Resource,
		Severity:    mapSeverity(ev.Severity),
	}
	for k, v := range ev.Detail {
		c.Fields = append(c.Fields, dgField{Name: k, Value: v, Inline: true})
	}
	return c
}

func mapSeverity(s string) string {
	switch strings.ToLower(s) {
	case "ok", "completed", "success":
		return "success"
	case "warn", "warning", "degraded":
		return "warn"
	case "error", "failed", "critical":
		return "critical"
	default:
		return "info"
	}
}

// enqueueDiscordGwEvent is the entry point that
// notifications.sendNotification calls when a channel of kind
// "discord-gateway" needs to fire. Non-blocking — drops on a full
// queue and bumps DroppedEvents so the UI surfaces the loss.
func (s *Store) enqueueDiscordGwEvent(ev *models.NotificationEvent) {
	s.mu.RLock()
	q := s.discordGwQueue
	s.mu.RUnlock()
	if q == nil {
		return
	}
	select {
	case q <- ev:
	default:
		s.patchDiscordGateway(func(g *models.DiscordGateway) {
			g.DroppedEvents++
		})
	}
}

// maybeShipAuditToDiscord (v5.53.0) is called from AddAuditEvent to
// fan out audit log entries through the gateway when the operator's
// flipped on the firehose toggle. Honors ShipAuditFailedOnly so a
// busy fleet's successful-action stream doesn't drown the channel.
//
// Non-blocking, drop-on-full-queue. The gateway is a courier — losing
// audit events here is acceptable because the canonical record stays
// in /api/audit (and audit_shipping.go's syslog/HTTP path).
func (s *Store) maybeShipAuditToDiscord(ev *models.AuditEvent) {
	if ev == nil {
		return
	}
	cfg := s.discordGatewaySnapshot()
	if cfg == nil || !cfg.Enabled || !cfg.ShipAuditEvents {
		return
	}
	if cfg.ShipAuditFailedOnly && ev.Success {
		return
	}
	s.enqueueDiscordGwEvent(auditToNotification(ev))
}

// auditToNotification adapts a stored AuditEvent into the
// NotificationEvent shape the gateway send-worker already knows how
// to render. The original audit event isn't mutated.
func auditToNotification(ev *models.AuditEvent) *models.NotificationEvent {
	severity := "info"
	if !ev.Success {
		severity = "warn"
	}
	title := ev.Action
	if ev.Success {
		title = ev.Action + " — ok"
	} else {
		title = ev.Action + " — failed"
	}
	// Compose a one-line message: actor + resource + (error if any).
	// Detail fields land as inline embed fields downstream.
	parts := []string{}
	if ev.ActorName != "" {
		parts = append(parts, "actor: "+ev.ActorName)
	} else if ev.ActorID != "" {
		parts = append(parts, "actor: "+ev.ActorID)
	}
	if ev.ActorIP != "" {
		parts = append(parts, "ip: "+ev.ActorIP)
	}
	if ev.Resource != "" {
		parts = append(parts, "resource: "+ev.Resource)
	}
	if ev.Error != "" {
		parts = append(parts, "error: "+ev.Error)
	}
	msg := strings.Join(parts, " · ")

	// Copy detail map verbatim so the gateway can surface it as
	// inline fields. Skip the "via" key since actor/ip already
	// covers that context.
	detail := map[string]string{}
	for k, v := range ev.Detail {
		if k == "via" {
			continue
		}
		detail[k] = v
	}

	return &models.NotificationEvent{
		Type:     "audit." + ev.Action,
		Title:    title,
		Message:  msg,
		Resource: ev.Resource,
		Severity: severity,
		At:       ev.At,
		Detail:   detail,
	}
}

// ─── snapshot for API surface (token redacted) ───────────────────

// discordGatewayPublicSnapshot returns a copy with Token redacted
// (UI gets a placeholder so the form can show "(configured)").
func (s *Store) discordGatewayPublicSnapshot() *models.DiscordGateway {
	cp := s.discordGatewaySnapshot()
	if cp == nil {
		return nil
	}
	if cp.Token != "" {
		cp.Token = "(configured)"
	}
	return cp
}

// ─── one-time test send ──────────────────────────────────────────

// sendTestCard ships a synthetic card the operator can use to
// confirm the wiring without waiting for a real notification.
func (c *gatewayClient) sendTestCard(ctx context.Context) error {
	card := dgCard{
		Title:       "kvm-center — gateway test",
		Description: "This is a test card sent from kvm-center's Settings → Notifications → Discord Gateway → **Test send**.",
		Severity:    "info",
		Type:        "audit.gateway.test",
		Resource:    "discord-gateway",
		Fields: []dgField{
			{Name: "Sent at", Value: time.Now().UTC().Format(time.RFC3339), Inline: true},
		},
	}
	_, err := c.sendCard(ctx, card)
	return err
}
