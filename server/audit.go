package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"sync"
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
	AuditLogin           = "user.login"
	AuditLogout          = "user.logout"
	AuditUserCreate      = "user.create"
	AuditUserUpdate      = "user.update"
	AuditUserDelete      = "user.delete"
	AuditGroupCreate     = "group.create"
	AuditGroupUpdate     = "group.update"
	AuditGroupDelete     = "group.delete"
	AuditRoleCreate      = "role.create"
	AuditRoleUpdate      = "role.update"
	AuditRoleDelete      = "role.delete"
	AuditIDPCreate       = "idp.create"
	AuditIDPUpdate       = "idp.update"
	AuditIDPDelete       = "idp.delete"
	AuditFeatures        = "features.update"
	AuditTLSUpload       = "tls.upload"
	AuditTLSRegenerate   = "tls.regenerate"
	AuditDBMigrate       = "db.migrate"
	AuditVMNotes         = "vm.notes"
	AuditVMCreate        = "vm.create"
	AuditVMDelete        = "vm.delete"
	AuditVMAction        = "vm.action" // START/STOP/RESTART/FORCESTOP; detail carries the verb
	AuditVMSnapshot      = "vm.snapshot"
	AuditVMRevert        = "vm.snapshot.revert"
	AuditVMSnapDelete    = "vm.snapshot.delete"
	AuditMigrationStart  = "migration.start"
	AuditMigrationCancel = "migration.cancel"
	// Grant lifecycle (v5.11). Three outcomes worth recording:
	//   .issue     — server minted a grant for a migration
	//   .accept    — destination agent validated the grant
	//   .reject    — destination refused (bad sig / expired / binding
	//                mismatch / replay)
	AuditMigrationGrantIssue  = "migration.grant.issue"
	AuditMigrationGrantAccept = "migration.grant.accept"
	AuditMigrationGrantReject = "migration.grant.reject"
	AuditConsoleOpen          = "vm.console"
	AuditHostMaintEnter       = "host.maintenance.enter"
	AuditHostMaintExit        = "host.maintenance.exit"
	AuditHostDelete           = "host.delete"
	AuditHostUpdateAgent      = "host.update_agent"
	// v5.56.0 — operator-acknowledged "this host is down on purpose".
	// Auto-cleared on recovery (recovery doesn't emit a separate
	// audit; host.recovered notification covers that side).
	AuditHostExpectedDownSet   = "host.expected_down.set"
	AuditHostExpectedDownClear = "host.expected_down.clear"
	// v5.58.0 — storage-pool lifecycle (define / start / stop /
	// autostart / no-autostart / delete) on a hypervisor.
	AuditStoragePool   = "storage.pool"
	AuditBackupExport  = "backup.export"
	AuditBackupRestore = "backup.restore"
	AuditKVMToolsQueue = "kvm-tools.queue-update"
	AuditAuditShipping = "audit.shipping.update"
	// Discord Gateway lifecycle (v5.52.0). Separate family from
	// audit-shipping so post-incident review can tell them apart and
	// the operator can subscribe a notification rule to just one.
	AuditDiscordGwUpdate = "discord.gateway.update"
	AuditDiscordGwPair   = "discord.gateway.pair"
	AuditDiscordGwRevoke = "discord.gateway.revoke"
	AuditDiscordGwTest   = "discord.gateway.test"
	// v5.60.15 — 2FA lifecycle. A stolen session can no longer
	// silently strip 2FA off an account; every transition is now
	// recorded.
	AuditTOTPSetup   = "user.totp.setup"
	AuditTOTPEnable  = "user.totp.enable"
	AuditTOTPDisable = "user.totp.disable"
	// v5.60.15 — RBAC denial. Emitted by requirePerm + the
	// /api/command perm gate when an authenticated principal lacks
	// the required permission, or when the request was completely
	// unauthenticated. Throttled at the audit-shipping layer so a
	// scanner can't firehose the log.
	AuditAuthDenied          = "auth.denied"
	AuditAuthUnauthenticated = "auth.unauthenticated"
)

// AddAuditEvent appends a new event, trims if we're over retention.
// Acquires s.mu internally — callers MUST NOT be holding it. (The
// pre-v5.60.18 doc-comment claimed callers were responsible for
// locking, which was wrong by inspection: the function acquires
// the lock itself. Trusting that comment was a self-deadlock trap.)
func (s *Store) AddAuditEvent(ev *models.AuditEvent) {
	if ev == nil {
		return
	}
	if ev.ID == "" {
		ev.ID = newAuditID()
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC().Truncate(time.Millisecond)
	}
	s.mu.Lock()
	for _, existing := range s.audit {
		if existing.ID == ev.ID {
			s.mu.Unlock()
			log.Printf("audit: duplicate identity refused")
			return
		}
	}
	if err := validateAuditSnapshot([]*models.AuditEvent{ev}); err != nil {
		s.mu.Unlock()
		log.Printf("audit: invalid event refused")
		return
	}
	s.audit = insertAudit(s.audit, ev)
	if len(s.audit) > auditRetentionMax {
		// Drop the oldest entries so retention stays bounded. We trim
		// in chunks to avoid a re-alloc storm at steady state.
		drop := len(s.audit) - auditRetentionMax
		s.audit = s.audit[drop:]
	}
	s.mu.Unlock()
	// Async persist so log-heavy requests don't block on Mongo write.
	s.AsyncSave()
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
	// Discord-gateway firehose (v5.53.0). Same drop-on-floor discipline
	// — a slow Discord rate-limit must never back up the audit-write
	// path.  See discord_gateway.go for the converter + filter.
	s.maybeShipAuditToDiscord(ev)
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
	ActorID  string
	Action   string
	Resource string
	Since    time.Time
	Until    time.Time
	Success  *bool // nil = both
	Offset   int
	Limit    int
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
		// v5.51+: surface the token's scopes on every event so a
		// post-incident review can tell whether a token was full-access
		// or narrowed. Empty → legacy/unscoped token; explicit listing
		// → tells the reviewer "only these perms could ever fire from
		// this token".
		if scopes := tokenScopesFromRequest(r); len(scopes) > 0 {
			dd["token_scopes"] = strings.Join(scopes, ",")
		}
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

// auditAuthDenied (v5.60.15+) records an authorisation failure with
// per-(actor, perm, reason) throttling so a scanner / misconfigured
// client can't firehose the audit log. The throttle is in-memory only
// — restart wipes it, which is acceptable: a scanner that survives a
// process restart logs one event again per restart.
var (
	authDenyMu   sync.Mutex
	authDenyLast = map[string]time.Time{}
)

const authDenyThrottle = 30 * time.Second

func auditAuthDenied(store *Store, r *http.Request, actorID, perm, reason string) {
	if store == nil {
		return
	}
	key := actorID + "|" + perm + "|" + reason
	authDenyMu.Lock()
	last, ok := authDenyLast[key]
	now := time.Now()
	if ok && now.Sub(last) < authDenyThrottle {
		authDenyMu.Unlock()
		return
	}
	authDenyLast[key] = now
	// Cheap cap so the map doesn't grow without bound.
	if len(authDenyLast) > 1024 {
		for k, t := range authDenyLast {
			if now.Sub(t) > authDenyThrottle {
				delete(authDenyLast, k)
			}
		}
	}
	authDenyMu.Unlock()

	action := AuditAuthDenied
	if actorID == "" {
		action = AuditAuthUnauthenticated
	}
	detail := map[string]string{
		"perm":   perm,
		"reason": reason,
		"path":   r.URL.Path,
		"method": r.Method,
		"ip":     clientIP(r),
	}
	ev := &models.AuditEvent{
		Action:   action,
		Resource: r.URL.Path,
		ActorID:  actorID,
		Success:  false,
		Detail:   detail,
	}
	store.AddAuditEvent(ev)
}

// recordOperatorTask (v5.60.10+) writes an entry into commandLog so
// operator-meaningful server-side mutations land in the Recent Tasks
// pane alongside agent-routed /api/command actions.
//
// Pre-v5.60.10, Recent Tasks was fed exclusively from /api/command —
// i.e. only VM-level actions the server routed to an agent. Host
// retire, snapshot-policy CRUD, enrolment approve / reject, etc.
// landed in the audit log but never the Recent Tasks pane, so an
// operator who'd just retired a host saw nothing in the pane and
// reasonably assumed it had been dropped. This helper closes that
// gap for a curated set of actions.
//
// `action` should be one of the AuditXXX constants (audit and task
// stay in sync). `resource` is the same shape the audit call uses
// (e.g. "host/<id>", "snapshot-policy/<id>"). `params` is operator-
// visible context (free-form key/value pairs the pane can render).
// `success=false` records a failed-task entry so a failed retire
// also shows in the pane.
//
// Call this AFTER the action completes, alongside the existing
// audit() / auditSystem() call. The two have separate retention
// (audit is firehose, commandLog has a cap + TTL) so the records
// won't conflict.
func recordOperatorTask(store *Store, r *http.Request, action, resource string, params map[string]string, success bool, errMsg string) {
	var actorID string
	if u := userFromRequest(r); u != nil {
		actorID = u.ID
	}
	outcome := "success"
	if !success {
		outcome = "failed"
	}
	now := time.Now().UTC()
	// Short, sortable, collision-resistant ID. Prefix `srv-` makes
	// the source obvious vs the operator-supplied request_id on
	// /api/command entries.
	reqID := "srv-" + action + "-" + hex.EncodeToString(func() []byte {
		var b [4]byte
		_, _ = rand.Read(b[:])
		return b[:]
	}())
	// commandLog.Record expects a HostID/VMUUID/VMName triple to
	// disambiguate per-target conflicts. For server-side tasks the
	// resource string is the canonical target — split it on the
	// first `/` if it looks like "<kind>/<id>" so the pane has
	// something useful to group by.
	hostID := ""
	if strings.HasPrefix(resource, "host/") {
		hostID = strings.TrimPrefix(resource, "host/")
	}
	entry := &models.CommandLogEntry{
		RequestID:   reqID,
		Action:      action,
		HostID:      hostID,
		Params:      params,
		ActorID:     actorID,
		SubmittedAt: now,
		AppliedAt:   &now,
		Outcome:     outcome,
		Message:     errMsg,
		StatusCode:  0,
	}
	store.commandLog.Record(entry)
}
