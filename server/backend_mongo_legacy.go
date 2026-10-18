package main

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"sort"
)

// Collection membership is a necessary gate, not full-schema approval. Keep
// checks at load/save/export too: cached connections cannot approve new data.
func rejectLegacyCollections(ctx context.Context, db *mongo.Database) error {
	if _, err := loadGuestGrowState(ctx, db); err != nil {
		return err
	}
	if _, err := loadGuestUpdateState(ctx, db); err != nil {
		return err
	}
	if _, err := loadAffinityState(ctx, db); err != nil {
		return err
	}
	if _, err := loadLegacyHistory(ctx, db); err != nil {
		return err
	}
	for _, name := range []string{"vm_tags"} {
		err := db.Collection(name).FindOne(ctx, bson.D{}, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Err()
		if err == nil {
			return fmt.Errorf("legacy %s collection requires explicit offline conversion", name)
		}
		if err != mongo.ErrNoDocuments {
			return err
		}
	}
	names, err := db.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return err
	}
	sort.Strings(names) // Stable refusal without choosing an arbitrary unknown namespace.
	for _, name := range names {
		if supportedMongoCollections[name] {
			continue
		}
		err := db.Collection(name).FindOne(ctx, bson.D{}, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Err()
		if err == nil {
			return fmt.Errorf("unsupported Mongo collection %q requires explicit offline preservation and compatibility review", name)
		}
		if err != mongo.ErrNoDocuments {
			return err
		}
	}
	return nil
}

// Every allowlisted namespace has an explicit current Load/Save representation.
// This does not approve every legacy field, value, index or operational behavior.
var supportedMongoCollections = map[string]bool{
	mcoHosts: true, mcoVMs: true, mcoStorage: true, mcoVMNotes: true, mcoGuestData: true,
	mcoMigrations: true, mcoCommandLog: true, mcoUsers: true, mcoGroups: true, mcoRoles: true,
	mcoSessions: true, mcoIDPs: true, mcoFeatures: true, mcoServerMeta: true, mcoAuditEvents: true,
	mcoSnapshotPolicies: true, mcoNotifications: true, mcoMetrics: true, mcoAuditShipping: true,
	mcoBackupSchedule: true, mcoDiscordGateway: true, mcoRemovedHosts: true, mcoPendingEnrol: true,
	mcoLegacyHistory: true, mcoLegacyHistoryContract: true,
	mcoAlarms: true, mcoAPITokens: true, mcoHypervisorCerts: true, mcoHostHistory: true, mcoVMTagState: true, "affinity_state": true, guestUpdateCollection: true, guestGrowCollection: true,
}
