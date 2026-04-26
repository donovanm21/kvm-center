package main

// Persistence layer methods for *Store — split out of store.go in
// v5.37.2 (P2 #2 first cut). The Save/Load/dirty-tracking flow is
// large enough to read on its own and lives at the boundary
// between the in-memory cache and the configured Backend. Methods
// stay on *Store so callers don't move; the file is a logical
// grouping, not a new package.

import (
	"encoding/json"
	"hash/fnv"
	"log"
	"sync"
	"time"
)

// lastHashes caches FNV hashes of each cold collection from the
// previous Save, letting us compute a dirty set automatically instead
// of instrumenting every mutator. Access is serialised by s.mu.
var lastHashes = struct {
	mu sync.Mutex
	m  map[string]uint64
}{m: map[string]uint64{}}

// computeDirty hashes each cold collection's JSON form, compares to
// the previous snapshot, and returns the set that changed. First call
// returns nil (save everything) so Mongo reaches a known-good state.
func computeDirty(d *persistData) map[string]bool {
	lastHashes.mu.Lock()
	defer lastHashes.mu.Unlock()
	first := len(lastHashes.m) == 0
	out := map[string]bool{}
	fields := map[string]any{
		"users":             d.Users,
		"groups":            d.Groups,
		"roles":             d.Roles,
		"idps":              d.IDPs,
		"sessions":          d.Sessions,
		"features":          d.Features,
		"server_meta":       d.ConsoleSecret,
		"notifications":     d.Notifications,
		"audit_shipping":    d.AuditShipping,
		"api_tokens":        d.APITokens,
		"snapshot_policies": d.SnapshotPolicies,
		"hypervisor_certs":  d.HypervisorCerts,
		"command_log":       d.CommandLog,
		"metrics":           d.Metrics,
	}
	for name, v := range fields {
		h := hashJSON(v)
		if first || lastHashes.m[name] != h {
			out[name] = true
		}
		lastHashes.m[name] = h
	}
	if first {
		return nil // nil == write everything on first Save
	}
	return out
}

func hashJSON(v any) uint64 {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
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
// computeDirty + the whole-state JSON marshal both run under the
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
	// Cheap pre-checks before the lock-held snapshot phase. The
	// nil-backend short-circuit matters in tests that pump
	// AsyncSave at high rate against an in-memory store — without
	// it, every save still serialises through snapshotBytes' read
	// lock, which under `-race` overhead piles up and starves
	// concurrent writers.
	if s.backend == nil {
		return
	}
	if !s.isSaveGated() {
		log.Printf("store: skipping Save — initial Load returned empty on a non-fresh install; resolve backend connectivity first")
		return
	}
	bytes, dirty, ok := s.snapshotBytes()
	if !ok {
		return
	}

	// Deep copy: unmarshal back into a fresh persistData. The backend
	// iterates ITS struct, never the live store maps.
	var snap persistData
	if err := json.Unmarshal(bytes, &snap); err != nil {
		log.Printf("store: snapshot unmarshal: %v", err)
		return
	}
	snap.Dirty = dirty // Dirty is `json:"-"`, reattach.

	ctx, cancel := contextWithTimeout(15 * time.Second)
	defer cancel()
	if err := s.backend.Save(ctx, &snap); err != nil {
		log.Printf("store: backend save error (%s): %v", s.backend.Name(), err)
	}
}

// snapshotBytes builds a persistData under the read lock, runs
// computeDirty, and serialises to JSON — all under the lock so
// concurrent writers can't race the iteration. Returns the bytes,
// the dirty set (which doesn't survive json round-trip), and a
// success flag.
func (s *Store) snapshotBytes() ([]byte, map[string]bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d := persistData{
		Hosts:            s.hosts,
		VMs:              s.vms,
		Storage:          s.storage,
		VMNotes:          s.vmNotes,
		GuestData:        s.guestData,
		Migrations:       s.migrations,
		Users:            s.users,
		Groups:           s.groups,
		Roles:            s.roles,
		Sessions:         s.sessions,
		IDPs:             s.idps,
		Features:         &s.features,
		Audit:            s.audit,
		AuditShipping:    s.auditShipping,
		SnapshotPolicies: s.snapshotPolicies,
		Notifications:    s.notifications,
		Metrics:          s.metrics,
		APITokens:        s.apiTokens,
		HypervisorCerts:  s.hypervisorCerts,
		CommandLog:       s.commandLog.snapshot(),
		ConsoleSecret:    s.consoleSecret,
	}
	d.Dirty = computeDirty(&d)
	bytes, err := json.Marshal(&d)
	if err != nil {
		log.Printf("store: snapshot marshal: %v", err)
		return nil, nil, false
	}
	return bytes, d.Dirty, true
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
		log.Printf("store: backend load error (%s): %v — periodic Save will be skipped until next successful boot Load", s.backend.Name(), err)
		return
	}
	if d == nil {
		// Truly fresh install — no persistData at all. Allow saves.
		s.saveGateOpen = true
		return
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
	if d.Audit != nil {
		s.audit = d.Audit
	}
	if d.AuditShipping != nil {
		s.auditShipping = d.AuditShipping
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
// tearing down TempDir. Production code fires and forgets; the
// WaitGroup is inert at runtime.
func (s *Store) AsyncSave() {
	s.saveWG.Add(1)
	go func() {
		defer s.saveWG.Done()
		s.Save()
	}()
}
