package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// Notifications (v5.4). Operators wire one or more channels (webhook
// or email) and toggle which event types fan out. A small worker
// reads from notifyQueue, fans each event to every enabled channel
// whose event type is on, and records the result on the channel for
// the UI to display.

const (
	notifyQueueSize = 256

	NotifyVMCrashed       = "vm.crashed"
	NotifyVMRecovered     = "vm.recovered"
	NotifyHostOffline     = "host.offline"
	NotifyHostRecovered   = "host.recovered"
	NotifyMigrationDone   = "migration.complete"
	NotifyMigrationFailed = "migration.failed"
	NotifyTLSExpiring     = "tls.expiring"
	NotifySnapPolicyFail  = "snapshot.policy.failed"
)

// AllNotifyTypes drives the UI checkbox list. Add new constants here
// when wiring new triggers.
var AllNotifyTypes = []string{
	NotifyVMCrashed,
	NotifyVMRecovered,
	NotifyHostOffline,
	NotifyHostRecovered,
	NotifyMigrationDone,
	NotifyMigrationFailed,
	NotifyTLSExpiring,
	NotifySnapPolicyFail,
}

// startNotificationDispatcher launches the worker once at boot. Safe
// to call before any config exists — the worker no-ops on an empty
// channel list.
func (s *Store) startNotificationDispatcher(ctx context.Context) {
	s.mu.Lock()
	if s.notifyQueue == nil {
		s.notifyQueue = make(chan *models.NotificationEvent, notifyQueueSize)
	}
	s.mu.Unlock()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-s.notifyQueue:
				s.dispatchNotification(ev)
			}
		}
	}()
}

// fireNotification is the single entry point every trigger calls.
// Non-blocking: if the queue is full, the event is dropped with a log
// line so a slow webhook can never back up the calling path.
func (s *Store) fireNotification(ev *models.NotificationEvent) {
	if s == nil || ev == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if s.notifyQueue == nil {
		return
	}
	select {
	case s.notifyQueue <- ev:
	default:
		log.Printf("notifications: queue full, dropping %s", ev.Type)
	}
}

func (s *Store) notificationsSnapshot() *models.NotificationConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.notifications == nil {
		return nil
	}
	cp := *s.notifications
	if s.notifications.EventTypes != nil {
		cp.EventTypes = make(map[string]bool, len(s.notifications.EventTypes))
		for k, v := range s.notifications.EventTypes {
			cp.EventTypes[k] = v
		}
	}
	if s.notifications.Channels != nil {
		cp.Channels = append([]*models.NotificationChannel(nil), s.notifications.Channels...)
	}
	return &cp
}

// GetNotifications returns the current config (defensive copy).
func (s *Store) GetNotifications() *models.NotificationConfig {
	return s.notificationsSnapshot()
}

// SetNotifications replaces the entire config. Validation lives in
// the handler.
func (s *Store) SetNotifications(cfg *models.NotificationConfig) {
	s.mu.Lock()
	s.notifications = cfg
	s.mu.Unlock()
	go s.Save()
}

func (s *Store) dispatchNotification(ev *models.NotificationEvent) {
	cfg := s.notificationsSnapshot()
	if cfg == nil || len(cfg.Channels) == 0 {
		return
	}
	if cfg.EventTypes == nil || !cfg.EventTypes[ev.Type] {
		return // operator hasn't subscribed to this event type
	}
	for _, ch := range cfg.Channels {
		if !ch.Enabled {
			continue
		}
		err := sendNotification(cfg, ch, ev)
		s.recordChannelResult(ch.ID, err)
		if err != nil {
			log.Printf("notifications: %s (%s) failed: %v", ch.Name, ch.Kind, err)
		}
	}
}

// sendNotification dispatches one event to one channel. Pure helper
// (no store reference) to keep the transport code easy to reason
// about and trivially testable.
func sendNotification(cfg *models.NotificationConfig, ch *models.NotificationChannel, ev *models.NotificationEvent) error {
	switch ch.Kind {
	case "webhook":
		return sendWebhook(ch, ev)
	case "email":
		return sendEmail(cfg, ch, ev)
	default:
		return fmt.Errorf("unknown channel kind %q", ch.Kind)
	}
}

func sendWebhook(ch *models.NotificationChannel, ev *models.NotificationEvent) error {
	if ch.WebhookURL == "" {
		return errors.New("webhook url required")
	}
	body, err := webhookPayload(ch.WebhookURL, ev)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, ch.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kvm-center/"+Version)
	if ch.WebhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(ch.WebhookSecret))
		mac.Write(body)
		req.Header.Set("X-KVMCenter-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s replied %d", ch.WebhookURL, resp.StatusCode)
	}
	return nil
}

// webhookPayload picks a body shape compatible with the destination.
// Discord and Slack want a chat-message envelope; everything else
// gets the raw event JSON so generic receivers (n8n, Zapier, custom
// HTTP listeners) keep all the structured fields.
func webhookPayload(rawURL string, ev *models.NotificationEvent) ([]byte, error) {
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = strings.ToLower(u.Host)
	}
	icon := severityIcon(ev.Severity)
	text := icon + " **" + ev.Title + "**\n" + ev.Message
	if ev.Resource != "" {
		text += "\n_" + ev.Resource + "_"
	}
	switch {
	case strings.HasSuffix(host, "discord.com") || strings.HasSuffix(host, "discordapp.com"):
		// Discord caps content at 2000 chars and rejects empty bodies.
		if len(text) > 1900 {
			text = text[:1900] + "…"
		}
		return json.Marshal(map[string]any{
			"username": "kvm-center",
			"content":  text,
		})
	case strings.HasSuffix(host, "slack.com"):
		return json.Marshal(map[string]any{
			"text":     text,
			"username": "kvm-center",
		})
	default:
		return json.Marshal(ev)
	}
}

func severityIcon(sev string) string {
	switch sev {
	case "critical":
		return "🔴"
	case "warning":
		return "🟡"
	default:
		return "🟢"
	}
}

func sendEmail(cfg *models.NotificationConfig, ch *models.NotificationChannel, ev *models.NotificationEvent) error {
	if cfg.SMTPHost == "" {
		return errors.New("smtp_host not configured (Notifications → SMTP)")
	}
	if ch.EmailTo == "" {
		return errors.New("email_to required on channel")
	}
	from := ch.EmailFrom
	if from == "" {
		from = cfg.SMTPFrom
	}
	if from == "" {
		return errors.New("email_from required (channel or global SMTP From)")
	}
	port := cfg.SMTPPort
	if port == 0 {
		port = 587
	}
	addr := cfg.SMTPHost + ":" + strconv.Itoa(port)

	tos := splitAndTrim(ch.EmailTo, ",")
	subject := "[" + strings.ToUpper(ev.Severity) + "] " + ev.Title
	var body bytes.Buffer
	body.WriteString("From: " + from + "\r\n")
	body.WriteString("To: " + strings.Join(tos, ", ") + "\r\n")
	body.WriteString("Subject: " + subject + "\r\n")
	body.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	body.WriteString(ev.Message + "\n\n")
	body.WriteString("Type: " + ev.Type + "\n")
	body.WriteString("Resource: " + ev.Resource + "\n")
	body.WriteString("At: " + ev.At.Format(time.RFC3339) + "\n")
	for k, v := range ev.Detail {
		body.WriteString(k + ": " + v + "\n")
	}

	var auth smtp.Auth
	if cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)
	}
	return smtp.SendMail(addr, auth, from, tos, body.Bytes())
}

func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) recordChannelResult(chID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notifications == nil {
		return
	}
	for _, ch := range s.notifications.Channels {
		if ch.ID != chID {
			continue
		}
		ch.LastSendAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			ch.LastSendError = err.Error()
		} else {
			ch.LastSendError = ""
			ch.LastSendCount++
		}
		return
	}
}

func newNotifyChannelID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ch_" + hex.EncodeToString(b[:])
}

// ── HTTP handlers ──────────────────────────────────────────────────

const (
	AuditNotifyUpdate = "notifications.update"
	AuditNotifyTest   = "notifications.test"
)

func handleNotifications(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			cfg := store.GetNotifications()
			if cfg == nil {
				cfg = &models.NotificationConfig{
					EventTypes: map[string]bool{},
					Channels:   []*models.NotificationChannel{},
				}
			}
			// Redact passwords on read.
			if cfg.SMTPPassword != "" {
				cfg.SMTPPassword = "********"
			}
			for _, ch := range cfg.Channels {
				if ch.WebhookSecret != "" {
					ch.WebhookSecret = "********"
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"config":     cfg,
				"all_types":  AllNotifyTypes,
			})
		case http.MethodPut:
			var req models.NotificationConfig
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
				return
			}
			// Preserve secrets when the client sends the masked
			// placeholder back unchanged.
			existing := store.GetNotifications()
			if req.SMTPPassword == "********" && existing != nil {
				req.SMTPPassword = existing.SMTPPassword
			}
			existingByID := map[string]*models.NotificationChannel{}
			if existing != nil {
				for _, c := range existing.Channels {
					existingByID[c.ID] = c
				}
			}
			for _, ch := range req.Channels {
				if ch.ID == "" {
					ch.ID = newNotifyChannelID()
				}
				if ch.WebhookSecret == "********" {
					if old, ok := existingByID[ch.ID]; ok {
						ch.WebhookSecret = old.WebhookSecret
					} else {
						ch.WebhookSecret = ""
					}
				}
				// Carry forward observability fields from existing channel.
				if old, ok := existingByID[ch.ID]; ok {
					ch.LastSendAt = old.LastSendAt
					ch.LastSendError = old.LastSendError
					ch.LastSendCount = old.LastSendCount
				}
			}
			if req.EventTypes == nil {
				req.EventTypes = map[string]bool{}
			}
			store.SetNotifications(&req)
			audit(store, r, AuditNotifyUpdate, "notifications", map[string]string{
				"channels": strconv.Itoa(len(req.Channels)),
			}, true, "")
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			writeError(w, http.StatusMethodNotAllowed, "GET / PUT only")
		}
	}
}

// handleNotificationTest fires a synthetic event through a single
// channel — operator-friendly "did this actually work" button.
func handleNotificationTest(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			ChannelID string `json:"channel_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cfg := store.GetNotifications()
		if cfg == nil {
			writeError(w, http.StatusNotFound, "no notification config")
			return
		}
		var target *models.NotificationChannel
		for _, c := range cfg.Channels {
			if c.ID == req.ChannelID {
				target = c
				break
			}
		}
		if target == nil {
			writeError(w, http.StatusNotFound, "channel not found")
			return
		}
		ev := &models.NotificationEvent{
			Type:     "test",
			Title:    "kvm-center test notification",
			Message:  "If you can read this, " + target.Name + " is wired up correctly.",
			Severity: "info",
			At:       time.Now().UTC(),
		}
		// Pull the unredacted config for the actual send.
		fullCfg := func() *models.NotificationConfig {
			store.mu.RLock()
			defer store.mu.RUnlock()
			if store.notifications == nil {
				return cfg
			}
			cp := *store.notifications
			return &cp
		}()
		err := sendNotification(fullCfg, target, ev)
		store.recordChannelResult(target.ID, err)
		if err != nil {
			audit(store, r, AuditNotifyTest, "channel/"+target.ID, nil, false, err.Error())
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		audit(store, r, AuditNotifyTest, "channel/"+target.ID, nil, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
	}
}
