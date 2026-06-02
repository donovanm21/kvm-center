package main

// Migration record methods on *Store — split out of store.go in
// v5.38.0 (P2 #2 part 2). Pure move; same locking/notification
// semantics. Methods stay on *Store so callers don't move; the
// file is a logical grouping.

import (
	"fmt"
	"time"

	"kvm-center/internal/models"
)

const (
	migrationSliceCap      = 1000
	migrationRetentionDays = 30
)

// isTerminalMigration matches the Status values we treat as "will
// never change again" for retention + trim purposes. Defined here
// rather than in models so the backend + store share one source of
// truth.
func isTerminalMigration(status string) bool {
	return status == "complete" || status == "failed" || status == "cancelled"
}

// addMigrationLocked inserts / upserts a migration record. Caller
// MUST hold s.mu.Lock(). Returns whether this call is responsible
// for firing the terminal-state notification (i.e. this is the
// first time the record has flipped to complete/failed). The
// notification itself is fired by the caller after it drops the
// lock, keeping the lock-held section tight.
//
// Introduced v5.36 (P0.2) so finalizeMigration + pollMigrationStatus
// can do their "mutate rec fields + upsert the record" pairs inside
// a single critical section, eliminating the torn-read window a
// concurrent migrations snapshot could otherwise observe.
func (s *Store) addMigrationLocked(r *models.MigrationRecord) (shouldNotify bool) {
	found := false
	for i, m := range s.migrations {
		if m.ID == r.ID {
			s.migrations[i] = r
			found = true
			break
		}
	}
	if !found {
		s.migrations = append(s.migrations, r)
		s.migrations = trimMigrations(s.migrations, migrationSliceCap)
	}
	// Fire-once tracker for terminal-state notifications. Callers
	// often mutate the same *r in place before calling us, so a
	// prevStatus comparison off the slice doesn't work — the slice
	// already holds the new value. A per-ID notified set is the
	// simple, correct alternative.
	if r.Status == "complete" || r.Status == "failed" {
		if s.migrationNotified == nil {
			s.migrationNotified = map[string]bool{}
		}
		if !s.migrationNotified[r.ID] {
			s.migrationNotified[r.ID] = true
			shouldNotify = true
		}
	}
	return shouldNotify
}

// UpdateMigrationProgress applies non-terminal polling updates
// (phase / progress / detail) to an existing record atomically.
// The slice upsert and the *MigrationRecord mutations happen under
// one write-lock so a concurrent GetMigrations snapshot can never
// observe a torn intermediate state. Tier 1 P0.2, v5.36.
func (s *Store) UpdateMigrationProgress(rec *models.MigrationRecord, phase string, progress int, detail string) {
	if rec == nil {
		return
	}
	s.mu.Lock()
	rec.Phase = phase
	rec.Progress = progress
	rec.Detail = detail
	shouldNotify := s.addMigrationLocked(rec)
	s.mu.Unlock()
	s.hub.Publish(EventMigration, map[string]any{"id": rec.ID, "status": rec.Status})
	_ = shouldNotify // non-terminal path doesn't notify
}

// FinalizeMigration stamps a record as terminal (complete / failed)
// and upserts it in one atomic section. Fires the terminal
// notification after releasing the lock — matches the behaviour of
// AddMigration's pre-v5.36 fire-once path.
func (s *Store) FinalizeMigration(rec *models.MigrationRecord, status, detail string) {
	if rec == nil {
		return
	}
	s.mu.Lock()
	rec.Status = status
	rec.CompletedAt = time.Now().Unix()
	rec.ElapsedSec = rec.CompletedAt - rec.StartedAt
	if status == "complete" {
		rec.Progress = 100
		rec.Phase = "complete"
	} else {
		rec.Phase = "failed"
		rec.Error = detail
	}
	if detail != "" {
		rec.Detail = detail
	}
	shouldNotify := s.addMigrationLocked(rec)
	s.mu.Unlock()
	s.hub.Publish(EventMigration, map[string]any{"id": rec.ID, "status": rec.Status})
	if shouldNotify {
		switch rec.Status {
		case "complete":
			s.fireNotification(&models.NotificationEvent{
				Type:     NotifyMigrationDone,
				Title:    "Migration complete: " + rec.VMName,
				Message:  fmt.Sprintf("%s migrated %s → %s in %ds.", rec.VMName, rec.SourceHost, rec.DestHost, rec.ElapsedSec),
				Resource: "migration/" + rec.ID,
				Severity: "info",
			})
		case "failed":
			s.fireNotification(&models.NotificationEvent{
				Type:     NotifyMigrationFailed,
				Title:    "Migration failed: " + rec.VMName,
				Message:  fmt.Sprintf("%s migration %s → %s failed: %s", rec.VMName, rec.SourceHost, rec.DestHost, rec.Error),
				Resource: "migration/" + rec.ID,
				Severity: "critical",
				Detail:   map[string]string{"error": rec.Error, "phase": rec.Phase},
			})
		}
	}
}

// AddMigration inserts or updates a migration record.
func (s *Store) AddMigration(r *models.MigrationRecord) {
	s.mu.Lock()
	shouldNotify := s.addMigrationLocked(r)
	s.mu.Unlock()
	s.hub.Publish(EventMigration, map[string]any{"id": r.ID, "status": r.Status})

	if shouldNotify {
		switch r.Status {
		case "complete":
			s.fireNotification(&models.NotificationEvent{
				Type:     NotifyMigrationDone,
				Title:    "Migration complete: " + r.VMName,
				Message:  fmt.Sprintf("%s migrated %s → %s in %ds.", r.VMName, r.SourceHost, r.DestHost, r.ElapsedSec),
				Resource: "migration/" + r.ID,
				Severity: "info",
			})
		case "failed":
			s.fireNotification(&models.NotificationEvent{
				Type:     NotifyMigrationFailed,
				Title:    "Migration failed: " + r.VMName,
				Message:  fmt.Sprintf("%s migration %s → %s failed: %s", r.VMName, r.SourceHost, r.DestHost, r.Error),
				Resource: "migration/" + r.ID,
				Severity: "critical",
				Detail:   map[string]string{"error": r.Error, "phase": r.Phase},
			})
		}
	}
}

// trimMigrations enforces migrationSliceCap while preserving every
// active record. When the cap is exceeded it drops the oldest
// terminal-state (complete/failed) entries until len == cap or no
// more terminal entries remain. If somehow the cap is blown purely
// by active records, the slice grows past cap on purpose — losing
// an in-flight migration record is worse than a memory spike.
func trimMigrations(s []*models.MigrationRecord, cap int) []*models.MigrationRecord {
	if len(s) <= cap {
		return s
	}
	// Two-pass: collect indices of terminal entries oldest-first,
	// then drop until len == cap.
	drop := make(map[int]struct{})
	for i, m := range s {
		if m == nil {
			continue
		}
		if m.Status == "complete" || m.Status == "failed" || m.Status == "cancelled" {
			drop[i] = struct{}{}
			if len(s)-len(drop) <= cap {
				break
			}
		}
	}
	if len(drop) == 0 {
		return s
	}
	out := make([]*models.MigrationRecord, 0, len(s)-len(drop))
	for i, m := range s {
		if _, skip := drop[i]; skip {
			continue
		}
		out = append(out, m)
	}
	return out
}

// GetMigrations returns all migration records, newest first.
func (s *Store) GetMigrations() []*models.MigrationRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.MigrationRecord, len(s.migrations))
	copy(out, s.migrations)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
