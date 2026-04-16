package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/models"
)

// Audit log shipping (v5.2.1). Ships every recorded AuditEvent to an
// external aggregator (syslog server or HTTP endpoint) so compliance
// systems don't have to scrape /api/audit. The transport is
// best-effort: a slow/broken aggregator never blocks the UI write
// path (see AddAuditEvent's non-blocking enqueue) and failures are
// surfaced in the config's LastShipError field so operators notice.

// Defaults applied when the operator leaves fields blank.
const (
	defaultBatchSize    = 50
	defaultFlushSeconds = 10
	auditShipQueueSize  = 1024
)

// startAuditShipper launches the background worker. Called exactly
// once at server startup — the worker picks up config changes from
// s.auditShipping on every flush cycle, so rewriting the config
// doesn't need a restart.
func (s *Store) startAuditShipper(ctx context.Context) {
	s.mu.Lock()
	if s.auditShipQueue == nil {
		s.auditShipQueue = make(chan *models.AuditEvent, auditShipQueueSize)
	}
	s.mu.Unlock()

	go func() {
		batch := make([]*models.AuditEvent, 0, defaultBatchSize*2)
		flushTicker := time.NewTicker(1 * time.Second) // outer tick; actual flush cadence comes from config
		defer flushTicker.Stop()

		drain := func() {
			for {
				select {
				case ev := <-s.auditShipQueue:
					batch = append(batch, ev)
				default:
					return
				}
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-s.auditShipQueue:
				batch = append(batch, ev)
				drain()
			case <-flushTicker.C:
				// no-op; fall through to config-driven flush decision
			}

			cfg := s.auditShippingSnapshot()
			if cfg == nil || !cfg.Enabled || cfg.Type == "" {
				// Shipping disabled — drop any accumulated batch so we
				// don't hold stale events forever.
				if len(batch) > 0 {
					batch = batch[:0]
				}
				continue
			}
			size := cfg.BatchSize
			if size <= 0 {
				size = defaultBatchSize
			}
			flush := cfg.FlushSeconds
			if flush <= 0 {
				flush = defaultFlushSeconds
			}
			ready := len(batch) >= size || (len(batch) > 0 && shouldFlushByAge(batch[0].At, time.Duration(flush)*time.Second))
			if !ready {
				continue
			}
			if err := shipAuditBatch(cfg, batch); err != nil {
				s.recordShipResult(err, int64(len(batch)))
				log.Printf("audit-shipping: %s: %v (batch=%d)", cfg.Type, err, len(batch))
				// Keep the batch on failure; next iteration retries. If
				// it grows beyond queue-size we'll drop at enqueue time.
				if len(batch) > auditShipQueueSize*2 {
					// Give up holding — avoid unbounded memory growth
					// if the aggregator stays down for hours.
					log.Printf("audit-shipping: dropping %d events after sustained failure", len(batch))
					batch = batch[:0]
				}
				continue
			}
			s.recordShipResult(nil, int64(len(batch)))
			batch = batch[:0]
		}
	}()
}

func shouldFlushByAge(oldest time.Time, maxAge time.Duration) bool {
	return time.Since(oldest) >= maxAge
}

// auditShippingSnapshot returns a defensive copy so the worker can
// read without holding the store lock across a network call.
func (s *Store) auditShippingSnapshot() *models.AuditShipping {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.auditShipping == nil {
		return nil
	}
	cp := *s.auditShipping
	return &cp
}

// SetAuditShipping replaces the shipping config. Validated by the
// handler; this path just persists. Pass nil to disable.
func (s *Store) SetAuditShipping(cfg *models.AuditShipping) {
	s.mu.Lock()
	s.auditShipping = cfg
	s.mu.Unlock()
	s.AsyncSave()
}

// recordShipResult is invoked by the worker after every flush so the
// UI can display when shipping last ran and whether it succeeded.
func (s *Store) recordShipResult(err error, count int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auditShipping == nil {
		return
	}
	s.auditShipping.LastShipAt = time.Now().UTC().Format(time.RFC3339)
	s.auditShipping.LastShipCount = count
	if err != nil {
		s.auditShipping.LastShipError = err.Error()
	} else {
		s.auditShipping.LastShipError = ""
	}
}

// shipAuditBatch routes the batch to the right transport. Pure
// function (no store state) so it's easy to test in isolation.
func shipAuditBatch(cfg *models.AuditShipping, events []*models.AuditEvent) error {
	if len(events) == 0 {
		return nil
	}
	switch cfg.Type {
	case "http":
		return shipHTTP(cfg, events)
	case "syslog":
		return shipSyslog(cfg, events)
	default:
		return fmt.Errorf("unknown shipping type %q", cfg.Type)
	}
}

// shipHTTP posts {events:[…]} to the configured endpoint. Keeps the
// TLS posture of the caller: if they want skip-verify they can front
// the kvm-center with a proxy that terminates, we never lower the bar
// on outbound egress. Bearer or basic auth both supported.
func shipHTTP(cfg *models.AuditShipping, events []*models.AuditEvent) error {
	if cfg.Endpoint == "" {
		return errors.New("endpoint required")
	}
	body, err := json.Marshal(map[string]any{
		"events":     events,
		"shipped_at": time.Now().UTC().Format(time.RFC3339),
		"source":     "kvm-center",
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.BearerToken)
	} else if cfg.BasicAuth != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cfg.BasicAuth)))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s replied %d", cfg.Endpoint, resp.StatusCode)
	}
	return nil
}

// shipSyslog sends one RFC 5424 message per event. Endpoint format:
//
//	"udp://host:port"  | "tcp://host:port"  | "host:port" (defaults UDP)
//
// Structured data carries the event fields so downstream parsers
// (syslog-ng, rsyslog, Graylog) can index them without regex.
func shipSyslog(cfg *models.AuditShipping, events []*models.AuditEvent) error {
	if cfg.Endpoint == "" {
		return errors.New("endpoint required")
	}
	proto, hostport := "udp", cfg.Endpoint
	if u, err := url.Parse(cfg.Endpoint); err == nil && u.Scheme != "" {
		proto = u.Scheme
		hostport = u.Host
	}
	switch proto {
	case "udp", "tcp":
	default:
		return fmt.Errorf("unsupported syslog scheme %q (use udp:// or tcp://)", proto)
	}
	conn, err := net.DialTimeout(proto, hostport, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))

	facility := cfg.SyslogFacility
	if facility <= 0 {
		facility = 1 // user-level
	}
	app := cfg.SyslogAppName
	if app == "" {
		app = "kvm-center"
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "kvm-center"
	}
	var buf bytes.Buffer
	for _, ev := range events {
		severity := 6 // informational
		if !ev.Success {
			severity = 3 // error
		}
		pri := facility*8 + severity
		sd := syslogSD(ev)
		msg := fmt.Sprintf("action=%s resource=%s actor=%s success=%v",
			ev.Action, ev.Resource, ev.ActorName, ev.Success)
		line := fmt.Sprintf("<%d>1 %s %s %s - - %s %s\n",
			pri,
			ev.At.UTC().Format(time.RFC3339),
			host, app,
			sd, msg)
		buf.WriteString(line)
	}
	if _, err := conn.Write(buf.Bytes()); err != nil {
		return err
	}
	return nil
}

// syslogSD formats an AuditEvent as a single RFC 5424 SD-ELEMENT.
// Keys are sanitised (no spaces/braces/quotes) and values are
// escaped per the spec.
func syslogSD(ev *models.AuditEvent) string {
	pairs := []string{
		`id="` + sdEscape(ev.ID) + `"`,
		`actor_id="` + sdEscape(ev.ActorID) + `"`,
		`actor_name="` + sdEscape(ev.ActorName) + `"`,
		`actor_ip="` + sdEscape(ev.ActorIP) + `"`,
		`action="` + sdEscape(ev.Action) + `"`,
		`resource="` + sdEscape(ev.Resource) + `"`,
	}
	if ev.Error != "" {
		pairs = append(pairs, `error="`+sdEscape(ev.Error)+`"`)
	}
	// Fold detail map into the same SD element so every field is one-
	// level flat for aggregators that don't handle nested structures.
	for k, v := range ev.Detail {
		pairs = append(pairs, sdSafeKey(k)+`="`+sdEscape(v)+`"`)
	}
	return "[kvmcenter@32473 " + strings.Join(pairs, " ") + "]"
}

func sdEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `]`, `\]`)
	return r.Replace(s)
}

// sdSafeKey strips characters that aren't legal in RFC 5424 SD-NAME.
// ASCII alphanumerics + '_' + '-' only.
func sdSafeKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "k"
	}
	return b.String()
}

// Sanity import keep in case a future refactor loses the only
// reference — belt-and-suspenders.
var _ = sync.Mutex{}
