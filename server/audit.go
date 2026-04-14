package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// Audit logging for kvm-center (v5.2). One AuditEvent is produced by
// every mutation the UI exposes — the intent is that a compliance
// review can answer "who did what when" without grepping text logs.
// Writes go through the usual Store persistence, so they land in
// whatever backend the operator configured (JSON or Mongo).
//
// Retention: capped at auditRetentionMax events; older entries are
// trimmed off the front when a write pushes us over. Time-based
// retention (e.g. 90 days) is a v5.3 concern — the current ring-
// buffer approach is simpler and predictable in memory.

const auditRetentionMax = 10000

// Common action names. Using constants rather than free-form strings
// so the UI's filter dropdown has a canonical list and renames are
// grep-safe.
const (
	AuditLogin         = "user.login"
	AuditLogout        = "user.logout"
	AuditUserCreate    = "user.create"
	AuditUserUpdate    = "user.update"
	AuditUserDelete    = "user.delete"
	AuditGroupCreate   = "group.create"
	AuditGroupUpdate   = "group.update"
	AuditGroupDelete   = "group.delete"
	AuditRoleCreate    = "role.create"
	AuditRoleUpdate    = "role.update"
	AuditRoleDelete    = "role.delete"
	AuditIDPCreate     = "idp.create"
	AuditIDPUpdate     = "idp.update"
	AuditIDPDelete     = "idp.delete"
	AuditFeatures      = "features.update"
	AuditTLSUpload     = "tls.upload"
	AuditTLSRegenerate = "tls.regenerate"
	AuditDBMigrate     = "db.migrate"
	AuditVMNotes       = "vm.notes"
	AuditVMCreate      = "vm.create"
	AuditVMDelete      = "vm.delete"
	AuditVMAction      = "vm.action" // START/STOP/RESTART/FORCESTOP; detail carries the verb
	AuditVMSnapshot    = "vm.snapshot"
	AuditVMRevert      = "vm.snapshot.revert"
	AuditVMSnapDelete  = "vm.snapshot.delete"
	AuditMigrationStart = "migration.start"
	AuditMigrationCancel = "migration.cancel"
	AuditConsoleOpen   = "vm.console"
	AuditHostMaintEnter = "host.maintenance.enter"
	AuditHostMaintExit  = "host.maintenance.exit"
	AuditKVMToolsQueue  = "kvm-tools.queue-update"
	AuditAuditShipping  = "audit.shipping.update"
)

// AddAuditEvent appends a new event, trims if we're over retention.
// Called under s.mu (write lock).
func (s *Store) AddAuditEvent(ev *models.AuditEvent) {
	if ev == nil {
		return
	}
	if ev.ID == "" {
		ev.ID = newAuditID()
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	s.mu.Lock()
	s.audit = append(s.audit, ev)
	if len(s.audit) > auditRetentionMax {
		// Drop the oldest entries so retention stays bounded. We trim
		// in chunks to avoid a re-alloc storm at steady state.
		drop := len(s.audit) - auditRetentionMax
		s.audit = s.audit[drop:]
	}
	s.mu.Unlock()
	// Async persist so log-heavy requests don't block on Mongo write.
	go s.Save()
	// Best-effort enqueue to the shipper worker. Non-blocking — a
	// stuck aggregator must never back-pressure the audit-write path.
	if s.auditShipQueue != nil {
		select {
		case s.auditShipQueue <- ev:
		default:
			// Queue full; drop on the floor and log so the operator
			// knows to investigate the downstream sink.
			log.Printf("audit-shipping: queue full, dropped event id=%s action=%s", ev.ID, ev.Action)
		}
	}
}

// GetAuditEvents returns the most-recent-first slice, filtered by the
// optional criteria. Paginated by the caller.
func (s *Store) GetAuditEvents(filter AuditFilter) ([]*models.AuditEvent, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Iterate newest-first.
	out := make([]*models.AuditEvent, 0, len(s.audit))
	for i := len(s.audit) - 1; i >= 0; i-- {
		ev := s.audit[i]
		if !filter.matches(ev) {
			continue
		}
		out = append(out, ev)
	}
	total := len(out)
	// Apply offset/limit.
	if filter.Offset >= len(out) {
		return nil, total
	}
	out = out[filter.Offset:]
	if filter.Limit > 0 && filter.Limit < len(out) {
		out = out[:filter.Limit]
	}
	return out, total
}

// AuditFilter is matched case-insensitively where sensible.
type AuditFilter struct {
	ActorID string
	Action  string
	Resource string
	Since   time.Time
	Until   time.Time
	Success *bool // nil = both
	Offset  int
	Limit   int
}

func (f AuditFilter) matches(ev *models.AuditEvent) bool {
	if f.ActorID != "" && !strings.EqualFold(ev.ActorID, f.ActorID) {
		return false
	}
	if f.Action != "" && !strings.EqualFold(ev.Action, f.Action) {
		return false
	}
	if f.Resource != "" && !strings.Contains(strings.ToLower(ev.Resource), strings.ToLower(f.Resource)) {
		return false
	}
	if !f.Since.IsZero() && ev.At.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && ev.At.After(f.Until) {
		return false
	}
	if f.Success != nil && ev.Success != *f.Success {
		return false
	}
	return true
}

// audit is the one call every mutation handler makes. Keeps the
// instrumentation pattern consistent: look up the actor from the
// request, record their IP, stamp the event.
func audit(store *Store, r *http.Request, action, resource string, detail map[string]string, success bool, errMsg string) {
	u := userFromRequest(r)
	// Tag the auth method so the audit log distinguishes browser
	// sessions from CLI/CI bearer tokens. We mutate a copy of detail
	// to avoid bleeding into a caller-shared map.
	if tokenID := tokenIDFromRequest(r); tokenID != "" {
		dd := map[string]string{"via": "token:" + tokenID}
		for k, v := range detail {
			dd[k] = v
		}
		detail = dd
	}
	ev := &models.AuditEvent{
		Action:   action,
		Resource: resource,
		Detail:   detail,
		Success:  success,
		Error:    errMsg,
		ActorIP:  clientIP(r),
	}
	if u != nil {
		ev.ActorID = u.ID
		ev.ActorName = u.Username
	}
	store.AddAuditEvent(ev)
	log.Printf("audit: actor=%s action=%s resource=%s success=%v err=%s",
		ev.ActorName, action, resource, success, errMsg)
}

// auditSystem records an event with no HTTP context — used for
// background tasks like migration pollers finalising a record.
func auditSystem(store *Store, action, resource string, detail map[string]string, success bool, errMsg string) {
	store.AddAuditEvent(&models.AuditEvent{
		Action:    action,
		Resource:  resource,
		Detail:    detail,
		Success:   success,
		Error:     errMsg,
		ActorID:   "system",
		ActorName: "system",
	})
}

// auditAs is like audit() but takes an explicit actor — used by the
// login path, where there's no session yet and userFromRequest(r)
// would return nil.
func auditAs(store *Store, r *http.Request, actorID, actorName, action, resource string, detail map[string]string, success bool, errMsg string) {
	store.AddAuditEvent(&models.AuditEvent{
		ActorID:   actorID,
		ActorName: actorName,
		ActorIP:   clientIP(r),
		Action:    action,
		Resource:  resource,
		Detail:    detail,
		Success:   success,
		Error:     errMsg,
	})
}

func newAuditID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
