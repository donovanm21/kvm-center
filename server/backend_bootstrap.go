package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

const (
	mongoBootAttempts   = 5
	mongoBootRetryDelay = 3 * time.Second
)

// A configured Mongo database is authoritative. Startup failures never select
// a potentially stale JSON file. Retry readiness for a bounded cold start.
func bootstrapBackend(mongoURL, mongoDB, dataFile string) (Backend, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	return bootstrapBackendWith(ctx, mongoURL, dataFile, func() Backend {
		return NewMongoBackend(mongoURL, mongoDB)
	}, func(ctx context.Context) error {
		timer := time.NewTimer(mongoBootRetryDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

func closeBootBackend(b Backend) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = b.Close(ctx)
}

// Dependencies are explicit so readiness, load and migration failures can be
// exercised with synthetic backends without services or timing sleeps.
func bootstrapBackendWith(ctx context.Context, mongoURL, dataFile string, newMongo func() Backend, retry func(context.Context) error) (Backend, error) {
	if strings.TrimSpace(mongoURL) == "" {
		marker, err := os.ReadFile(dataFile + ".backend")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("backend selection marker unreadable; startup stopped")
		}
		kind := strings.TrimSpace(string(marker))
		if kind == "mongo" {
			return nil, errors.New("Mongo configuration missing for prior Mongo installation; restore configuration before startup")
		}
		if err == nil && kind != "json" {
			return nil, errors.New("backend selection marker unrecognized; startup stopped")
		}
		return NewJSONBackend(dataFile), nil
	}
	// A backup overlay requests explicit fresh-destination restore. Detect it
	// before Info/Load can initialize indexes on an existing destination.
	var overlayRestore bool
	if raw, err := os.ReadFile(dataFile); err == nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, errors.New("invalid existing recovery JSON; startup stopped before database initialization")
		}
		{
			for key := range fields {
				if strings.EqualFold(key, "entity_bson_overlay") || strings.EqualFold(key, "legacy_history_state") || strings.EqualFold(key, "affinity_state") || strings.EqualFold(key, "guest_update_state") {
					overlayRestore = true
				}
			}
		}
		if overlayRestore {
			if _, err := NewJSONBackend(dataFile).Load(ctx); err != nil {
				return nil, errors.New("invalid entity overlay recovery source")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("recovery source inaccessible")
	}
	var mb Backend
	for attempt := 1; attempt <= mongoBootAttempts; attempt++ {
		mb = newMongo()
		if overlayRestore {
			target, ok := mb.(*MongoBackend)
			if !ok {
				return nil, errors.New("entity overlay recovery requires Mongo destination")
			}
			if err := target.requireEmptyRestoreTarget(ctx); err != nil {
				closeBootBackend(mb)
				return nil, errors.New("entity overlay recovery target is not an empty accessible destination")
			}
		}
		readyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		info := mb.Info(readyCtx)
		cancel()
		if info.Healthy {
			break
		}
		closeBootBackend(mb)
		mb = nil
		// Driver errors can include credential-bearing connection details. Report
		// stage and attempt only, never the supplied URI or raw driver error.
		if attempt == mongoBootAttempts || ctx.Err() != nil {
			return nil, errors.New("Mongo readiness failed within startup retry budget; JSON fallback disabled")
		}
		log.Printf("mongo: not ready; retrying (attempt %d/%d)", attempt, mongoBootAttempts)
		if retry(ctx) != nil {
			return nil, errors.New("Mongo startup retry canceled; JSON fallback disabled")
		}
	}
	ok := false
	defer func() {
		if !ok {
			closeBootBackend(mb)
		}
	}()
	loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d, err := mb.Load(loadCtx)
	if err != nil {
		return nil, errors.New("Mongo initial load failed; JSON fallback disabled")
	}
	// Preserve the existing legacy migration policy in this narrow repair.
	// A separate reconciliation must replace the historical zero-host heuristic.
	if d == nil || len(d.Hosts) == 0 {
		if _, err := os.Stat(dataFile); err == nil {
			if err := migrateBackend(loadCtx, NewJSONBackend(dataFile), mb); err != nil {
				return nil, errors.New("Mongo legacy migration failed; JSON fallback disabled")
			}
			if err := os.Rename(dataFile, dataFile+".migrated"); err != nil {
				return nil, errors.New("Mongo migration completed but source archive failed; startup stopped for recovery")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("legacy migration source inaccessible; startup stopped")
		}
	}
	ok = true
	return mb, nil
}

// Validate the Store's actual hydration before listeners or background workers
// start. This catches a second Load failure after bootstrap readiness succeeds.
func initializeBootStore(backend Backend, dataFile string) (*Store, error) {
	store := NewStoreWithBackend(backend, dataFile)
	if !store.saveGateOpen {
		closeBootBackend(backend)
		return nil, fmt.Errorf("%s store hydration failed; startup stopped", backend.Name())
	}
	if backend.Name() == "mongo" {
		// Compatible with the existing private checkout's data.json.backend marker.
		// Record only after initialization; failure must not silently lose the guard.
		if err := recordMongoBackend(dataFile); err != nil {
			closeBootBackend(backend)
			return nil, errors.New("cannot record Mongo backend selection; startup stopped")
		}
	}
	return store, nil
}

// Replace the marker atomically; a failed write must not erase a prior selection.
func recordMongoBackend(dataFile string) error {
	target := dataFile + ".backend"
	f, err := os.CreateTemp(dirOf(dataFile), ".backend-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString("mongo\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), target)
}
