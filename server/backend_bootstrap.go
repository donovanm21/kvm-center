package main

import (
	"context"
	"log"
	"os"
	"time"
)

// bootstrapBackend picks the persistence backend at server startup
// based on the -mongo-url flag and automatically migrates legacy
// data.json content into Mongo on first-ever Mongo connect.
//
// Behaviour matrix:
//
//   mongoURL blank → JSONBackend over dataFile (v5.0 behaviour).
//
//   mongoURL set + Mongo reachable + Mongo empty + data.json exists →
//     one-shot migrate JSON → Mongo, rename data.json to
//     data.json.migrated so it's kept for rollback but won't double-load.
//
//   mongoURL set + Mongo reachable + (Mongo non-empty OR no data.json) →
//     MongoBackend alone.
//
//   mongoURL set + Mongo unreachable → fall back to JSONBackend with a
//     WARN so the operator still gets a working server and can fix the
//     Mongo config from Settings → Database.
func bootstrapBackend(mongoURL, mongoDB, dataFile string) Backend {
	if mongoURL == "" {
		return NewJSONBackend(dataFile)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mb := NewMongoBackend(mongoURL, mongoDB)
	// Info() side-effects the initial dial + ping + index creation —
	// catches auth failures, bad URLs, network issues at boot rather
	// than on the first write.
	info := mb.Info(ctx)
	if !info.Healthy {
		log.Printf("⚠ mongo: %s — falling back to JSON backend at %s (fix via Settings → Database)",
			info.Error, dataFile)
		return NewJSONBackend(dataFile)
	}

	// Check whether the Mongo side looks empty. "Empty" is defined as
	// zero host docs — a fresh Mongo has nothing, a migrated Mongo has
	// at least the hypervisors inventory.
	d, err := mb.Load(ctx)
	if err != nil {
		log.Printf("⚠ mongo: initial load failed (%v) — falling back to JSON", err)
		return NewJSONBackend(dataFile)
	}
	if d != nil && len(d.Hosts) > 0 {
		return mb
	}

	// Mongo is empty. If there's a populated data.json nearby, migrate
	// it in now. Otherwise start fresh in Mongo.
	if _, err := os.Stat(dataFile); err != nil {
		return mb
	}
	log.Printf("mongo: empty database detected — migrating %s into Mongo", dataFile)
	src := NewJSONBackend(dataFile)
	if err := migrateBackend(ctx, src, mb); err != nil {
		log.Printf("⚠ mongo: migration from %s failed (%v) — staying on JSON backend", dataFile, err)
		return src
	}
	// Rename the source file so a subsequent reboot doesn't re-migrate
	// and so we keep a known-good copy on disk for rollback.
	archived := dataFile + ".migrated"
	if err := os.Rename(dataFile, archived); err != nil {
		log.Printf("mongo: migrated OK but could not rename %s → %s: %v (harmless; rename yourself to avoid re-migration)",
			dataFile, archived, err)
	} else {
		log.Printf("mongo: migration complete, archived %s → %s", dataFile, archived)
	}
	return mb
}
