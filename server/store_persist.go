package main

// Persistence layer methods for *Store — split out of store.go in
// v5.37.2 (P2 #2 first cut). The Save/Load/dirty-tracking flow is
// large enough to read on its own and lives at the boundary
// between the in-memory cache and the configured Backend. Methods
// stay on *Store so callers don't move; the file is a logical
// grouping, not a new package.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"runtime/debug"
	"time"
)

// snapshotHashes fingerprints the exact serialized state, including fields
// excluded from API JSON. This is read-only: Save commits the result only
// after a successful backend write, so a failed write stays dirty on retry.
func snapshotHashes(raw []byte) (map[string]uint64, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	names := []string{
		"guest_grow_state", "guest_update_state", "affinity_state", "entity_bson_overlay", "legacy_history_state", "host_history_state", "vm_tag_state", "users", "groups", "roles", "idps", "sessions", "features",
		"notifications", "audit_shipping", "backup_schedule", "discord_gateway",
		"removed_hosts", "pending_enrollments", "api_tokens", "snapshot_policies",
		"hypervisor_certs", "command_log", "metrics", "console_secret",
	}
	hashes := make(map[string]uint64, len(names))
	for _, name := range names {
		h := fnv.New64a()
		_, _ = h.Write(fields[name])
		// Private state belongs to these collections. Conservatively dirty all
		// three when any private field changes; never skip a credential update.
		if name == "discord_gateway" || name == "pending_enrollments" || name == "command_log" {
			_, _ = h.Write([]byte{0})
			_, _ = h.Write(fields["_private"])
		}
		key := name
		if name == "console_secret" {
			key = "server_meta"
		}
		hashes[key] = h.Sum64()
	}
	return hashes, nil
}

func dirtyCollections(previous, current map[string]uint64) map[string]bool {
	if previous == nil {
		return nil
	} // First successful save writes everything.
	dirty := make(map[string]bool)
	for name, hash := range current {
		if previous[name] != hash {
			dirty[name] = true
		}
	}
	return dirty
}

// saveGated is true once the initial Load has populated the store
// from the backend. Until then, the periodic Save goroutine refuses
// to run — a transient backend read error at boot used to return an
// empty persistData, which the next 30s tick would happily Save back
// as empty, overwriting good data. Tier S #15, v5.32.18.
//
// Only read+written under s.mu. Flipped true at the end of load() if
// either: (a) the backend looked fresh (no hosts on record) OR (b)
// Load came back non-empty. If Load came back empty on what looks
// like an initialised store, we stay gated forever — the periodic
// tick no-ops and the operator will see log errors on every boot
// until they resolve the backend issue.
func (s *Store) isSaveGated() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.saveGateOpen
}

// Save persists the current store state through the configured
// backend (JSON file or Mongo).
//
// Lock discipline (v5.41.0, closes feedback_async_save.md):
// Fingerprinting and the whole-state JSON marshal both run under the
// read lock so nothing iterates the live pointer-valued maps
// without it. The marshalled bytes are deep-copied out — we
// unmarshal back into a fresh persistData struct that the backend
// owns end-to-end. That lets us release the lock before the
// (potentially seconds-long) Mongo / JSON-file write.
//
// Pre-fix the snapshot was built under RLock and the lock released
// before computeDirty + backend.Save iterated the same maps. The
// race detector caught it once concurrent AsyncSave + auth writes
// landed in the same test. The roundtrip pays one extra marshal +
// unmarshal per Save (~hundreds of µs per MB) which is cheap
// relative to the I/O it precedes.
func (s *Store) Save() {
	// Keep the in-memory test store's fire-and-forget saves inexpensive.
	if s.backend == nil {
		return
	}
	if err := s.SaveChecked(); err != nil {
		log.Printf("store: save failed: %v", err)
	}
}

// SaveChecked persists a consistent snapshot and reports any failure. Use
// it before acknowledging security changes or other durability-sensitive
// mutations; Save is only the logging wrapper for background persistence.
func (s *Store) SaveChecked() error {
	if !s.saves.begin() {
		return errors.New("store: persistence admission closed")
	}
	defer s.saves.end()
	return s.saveCheckedContext(context.Background())
}

func (s *Store) saveCheckedContext(ctx context.Context) error {
	if s.backend == nil {
		return errors.New("store: no persistence backend configured")
	}
	if !s.isSaveGated() {
		return errors.New("store: persistence is blocked until the initial backend load succeeds")
	}
	// v5.60.6: serialise inside Save itself, not at the AsyncSave
	// caller. v5.60.4 added the mutex at AsyncSave's goroutine entry,
	// but six direct Save() call sites (30s persist ticker, CA
	// handlers, restore, command-log flush) bypassed it — under load
	// they raced the AsyncSave goroutines and re-introduced the
	// E11000 dup-key cycle the v5.60.4 fix was meant to end.
	if err := s.saveMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.saveMu.Unlock()
	// v5.60.18: clear the AsyncSave debounce flag UNDER saveMu so a
	// mutator that lands now CASes true and spawns at most one
	// queued goroutine (which will block here on saveMu). Previously
	// AsyncSave's goroutine cleared saveQueued BEFORE acquiring
	// saveMu, which allowed unbounded fan-out: every mutator during
	// the window between clear-and-acquire passed the CAS gate and
	// spawned its own goroutine. Telemetry / migration bursts could
	// pile up dozens of goroutines on saveMu.
	s.saveQueued.Store(false)
	bytes, hashes, err := s.snapshotBytes()
	if err != nil {
		return err
	}

	// Deep copy: unmarshal back into a fresh persistData. The backend
	// iterates ITS struct, never the live store maps.
	var snap persistData
	if err := json.Unmarshal(bytes, &snap); err != nil {
		return fmt.Errorf("store: snapshot unmarshal: %w", err)
	}
	snap.Dirty = dirtyCollections(s.lastSavedHashes, hashes)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := s.backend.Save(ctx, &snap); err != nil {
		// A backend may have committed some collections before failing.
		// Force a full retry, including fields reverted to their last
		// successful value since that partial write.
		s.lastSavedHashes = nil
		return fmt.Errorf("store: backend save (%s): %w", s.backend.Name(), err)
	}
	if err := ctx.Err(); err != nil {
		s.lastSavedHashes = nil
		return err
	}
	s.mu.Lock()
	s.entityOverlay = snap.EntityOverlay
	s.mu.Unlock()
	s.lastSavedHashes = hashes
	return nil
}

// snapshotBytes serializes a complete, internally consistent copy under the
// read lock. Its fingerprints are not committed until backend.Save succeeds.
func (s *Store) snapshotBytes() ([]byte, map[string]uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d := s.persistDataLocked()
	raw, err := json.Marshal(&d)
	if err != nil {
		return nil, nil, fmt.Errorf("store: snapshot marshal: %w", err)
	}
	hashes, err := snapshotHashes(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("store: snapshot fingerprints: %w", err)
	}
	return raw, hashes, nil
}

// persistDataLocked is the single inventory of persisted fields for saves
// and backups. Caller must hold s.mu while using its pointer-valued fields.
func (s *Store) persistDataLocked() persistData {
	d := persistData{
		GuestGrows:         s.guestGrows,
		GuestUpdates:       s.guestUpdates,
		Alarms:             s.alarms,
		EntityOverlay:      s.entityOverlay,
		HostHistory:        s.historyStateLocked(),
		Affinity:           s.affinity,
		LegacyHistory:      s.legacyHistory,
		VMTagState:         s.tagStateLocked(),
		Hosts:              s.hosts,
		VMs:                s.vms,
		Storage:            s.storage,
		VMNotes:            s.vmNotes,
		GuestData:          s.guestData,
		Migrations:         s.migrations,
		Users:              s.users,
		Groups:             s.groups,
		Roles:              s.roles,
		Sessions:           s.sessions,
		IDPs:               s.idps,
		Features:           &s.features,
		Audit:              s.audit,
		AuditShipping:      s.auditShipping,
		SnapshotPolicies:   s.snapshotPolicies,
		Notifications:      s.notifications,
		Metrics:            s.metrics,
		APITokens:          s.apiTokens,
		HypervisorCerts:    s.hypervisorCerts,
		CommandLog:         s.commandLog.snapshot(),
		ConsoleSecret:      s.consoleSecret,
		BackupSchedule:     s.backupSchedule,
		DiscordGateway:     s.discordGateway,
		RemovedHosts:       s.removedHosts,
		PendingEnrollments: s.pendingEnrollments,
	}
	return d
}

func (s *Store) load() {
	if s.backend == nil {
		// No backend = in-memory test harness. Allow saves.
		s.saveGateOpen = true
		return
	}
	ctx, cancel := contextWithTimeout(15 * time.Second)
	defer cancel()
	d, err := s.backend.Load(ctx)
	if err != nil {
		// Load error: do NOT open the save gate. Operator fixes
		// backend; next restart retries Load. Periodic Save no-ops
		// until then, preserving whatever's in the backend.
		if s.backend.Name() == "mongo" {
			log.Printf("store: Mongo load failed; save gate remains closed")
		} else {
			log.Printf("store: backend load error (%s): %v — periodic Save will be skipped until next successful boot Load", s.backend.Name(), err)
		}
		return
	}
	if d == nil {
		// Truly fresh install — no persistData at all. Allow saves.
		s.saveGateOpen = true
		return
	}
	if err := d.Affinity.Validate(); err != nil {
		log.Printf("store: affinity state rejected")
		return
	}
	s.affinity = d.Affinity.Clone()
	if err := d.GuestUpdates.Validate(); err != nil {
		return
	}
	if err := validateGuestGrows(d.GuestGrows); err != nil {
		return
	}
	s.guestGrows = d.GuestGrows
	s.guestGrowOwners = nil
	s.guestGrowUncertain = false
	s.growSessions = nil
	s.guestUpdates = d.GuestUpdates
	s.guestUpdateOwners = nil
	s.guestUpdateUncertain = nil
	if err := d.LegacyHistory.Validate(); err != nil {
		log.Printf("store: legacy history load rejected")
		return
	}
	if err := d.VMTagState.validate(); err != nil {
		log.Printf("store: VM tag state load rejected: %v", err)
		return
	}
	if err := d.HostHistory.validate(); err != nil {
		log.Printf("store: host history load rejected: %v", err)
		return
	}
	s.alarms = cloneAlarms(d.Alarms)
	s.alarmStates = nil
	s.entityOverlay = d.EntityOverlay
	s.legacyHistory = d.LegacyHistory.Clone()
	s.history = cloneHistory(d.HostHistory)
	pruneHistory(s.history, time.Now().Unix())
	if d.VMTagState != nil {
		s.vmTags = cloneVMTags(d.VMTagState.ByVM)
	}
	if d.Hosts != nil {
		s.hosts = d.Hosts
	}
	if d.VMs != nil {
		s.vms = d.VMs
	}
	if d.Storage != nil {
		s.storage = d.Storage
	}
	if d.VMNotes != nil {
		s.vmNotes = d.VMNotes
	}
	if d.GuestData != nil {
		s.guestData = d.GuestData
	}
	if d.Migrations != nil {
		s.migrations = d.Migrations
	}
	if d.Users != nil {
		s.users = d.Users
	}
	if d.Groups != nil {
		s.groups = d.Groups
	}
	if d.Roles != nil {
		s.roles = d.Roles
	}
	if d.Sessions != nil {
		// Drop any that expired while we were down.
		now := time.Now()
		for tok, sess := range d.Sessions {
			if now.After(sess.ExpiresAt) {
				delete(d.Sessions, tok)
			}
		}
		s.sessions = d.Sessions
	}
	if d.IDPs != nil {
		s.idps = d.IDPs
	}
	if d.Features != nil {
		s.features = *d.Features
	}
	if d.ConsoleSecret != "" {
		s.consoleSecret = d.ConsoleSecret
	}
	if d.BackupSchedule != nil {
		s.backupSchedule = d.BackupSchedule
	}
	if d.Audit != nil {
		s.audit = d.Audit
		sortAudit(s.audit)
	}
	if d.AuditShipping != nil {
		s.auditShipping = d.AuditShipping
	}
	if d.DiscordGateway != nil {
		s.discordGateway = d.DiscordGateway
		// v5.53.1: defensive — a previously-paired singleton with an
		// empty Token at load time means something dropped the bearer
		// without a corresponding state transition (e.g. an aborted
		// re-pair, or persistence-layer dup-key bug clobbering the
		// singleton). Status would otherwise stay "healthy" and every
		// /webhook send silently fail "not paired". Flip to revoked so
		// the panel surfaces "Re-pair" instead of green-but-broken.
		//
		// v5.60.0: extended to also catch stuck `saving` (no
		// registration_id, no in-flight pairRequest goroutine to
		// resume across the restart) and stuck `pending` past the
		// 1h approval deadline. Without these, a server restart
		// mid-pair leaves the singleton frozen indefinitely; the
		// dropped_events counter increments per audit event and the
		// operator sees a yellow `saving` chip with no clear next
		// action. We saw 263 dropped events on hostD dogfood.
		if s.discordGateway.Enabled && s.discordGateway.Token == "" {
			st := s.discordGateway.Status
			ps := s.discordGateway.PendingSince
			stuck := false
			switch st {
			case "healthy", "approved", "connecting":
				stuck = true
			case "saving":
				// No reg_id + restart === pair-goroutine died.
				if s.discordGateway.RegistrationID == "" {
					stuck = true
				}
			case "pending":
				// > 1h since pair started + still no token === the
				// operator never approved. Surface the Re-pair button.
				if t, err := time.Parse(time.RFC3339, ps); err == nil {
					if time.Since(t) > 1*time.Hour {
						stuck = true
					}
				} else {
					stuck = true
				}
			}
			if stuck {
				s.discordGateway.Status = "revoked"
				s.discordGateway.LastSendError = "no bearer token on file — re-pair required"
			}
		}
	}
	if d.RemovedHosts != nil {
		s.removedHosts = d.RemovedHosts
	}
	if d.PendingEnrollments != nil {
		s.pendingEnrollments = d.PendingEnrollments
	}
	if d.SnapshotPolicies != nil {
		s.snapshotPolicies = d.SnapshotPolicies
	}
	if d.Notifications != nil {
		s.notifications = d.Notifications
	}
	if d.Metrics != nil {
		s.metrics = d.Metrics
	}
	if d.APITokens != nil {
		// Backfill kind on pre-v5.6 tokens. They were all minted from the
		// admin-gated endpoint so "system" preserves existing behavior.
		for _, t := range d.APITokens {
			if t.Kind == "" {
				t.Kind = "system"
			}
		}
		s.apiTokens = d.APITokens
	}
	if d.HypervisorCerts != nil {
		s.hypervisorCerts = d.HypervisorCerts
	}
	if d.CommandLog != nil {
		s.commandLog.replace(d.CommandLog)
	}
	// Non-empty-Load gate (Tier S #15). Open the save gate when the
	// Load result shows that either (a) this is a fresh install with
	// nothing to lose or (b) the backend successfully returned real
	// data. On a post-install empty Load — looks fresh but isn't —
	// we stay gated and the operator gets "skipping Save" log lines
	// on every tick until they resolve backend connectivity.
	//
	// Heuristic for "looks fresh": no hosts AND no users. Even a
	// single-admin-only bootstrap has a users entry.
	if len(s.hosts) > 0 || len(s.users) > 0 {
		s.saveGateOpen = true
	} else {
		log.Printf("store: initial Load returned empty on what looks like a fresh install — save gate open")
		s.saveGateOpen = true
	}
	log.Printf("store: loaded %d hosts, %d users, %d groups, %d roles from %s",
		len(s.hosts), len(s.users), len(s.groups), len(s.roles), s.dataFile)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 10)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// AsyncSave kicks off a non-blocking Save and registers it with
// saveWG so tests can wait for persistence to quiesce before
// tearing down TempDir. The saves work set independently tracks production
// drain and atomically seals admission once all accepted saves finish.
//
// Wrapped in a recover() so a panic during marshal / Mongo write
// (e.g. unexpected nil in the store from a buggy mutator) only
// kills this goroutine, not the whole server. Bare `go func()`
// would propagate the panic to the runtime and exit the process,
// which is overkill for a save that the next tick will retry.
//
// v5.60.4 — serialise + coalesce. Pre-fix, a burst of mutations
// (telemetry tick, mass migration, RBAC sync) spawned a goroutine
// per mutation and the goroutines raced inside the Mongo backend's
// DeleteMany+InsertMany pattern, tripping E11000 on every save and
// stranding state in memory. Now: at most one save is in flight
// (saveMu) and at most one is queued (saveQueued). A write that
// lands AFTER the in-flight save's snapshotBytes but BEFORE the
// queued slot opens will be picked up by the queued save —
// saveQueued is reset right before the queued save runs, so any
// AsyncSave during that brief window re-arms the next round.
func (s *Store) AsyncSave() {
	if !s.saves.begin() {
		return
	}
	if !s.saveQueued.CompareAndSwap(false, true) {
		s.saves.end()
		return // already queued; the in-flight save will catch any pending writes
	}
	s.saveWG.Add(1)
	go func() {
		defer s.saveWG.Done()
		defer s.saves.end()
		defer func() {
			if r := recover(); r != nil {
				s.saves.fail(errors.New("asynchronous persistence worker panicked"))
				log.Printf("worker: %q panicked: %v\n%s", "async-save", r, debug.Stack())
			}
		}()
		// v5.60.6: Save itself acquires saveMu — don't double-lock here.
		// v5.60.18: saveQueued is now cleared UNDER saveMu inside Save()
		// so this goroutine doesn't need (and must not) clear it here.
		// See the matching comment in Save() for the fan-out analysis.
		s.Save()
	}()
}
