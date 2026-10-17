package main

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// This is a narrow compatibility refusal, not a full-schema approval. Keep the
// Load-time checks too: a cached connection is not evidence of current schema.
func rejectLegacyCollections(ctx context.Context, db *mongo.Database) error {
	for _, name := range []string{"perf_history", "vm_tags"} {
		err := db.Collection(name).FindOne(ctx, bson.D{}, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Err()
		if err == nil {
			return fmt.Errorf("legacy %s collection requires explicit offline conversion", name)
		}
		if err != mongo.ErrNoDocuments {
			return err
		}
	}
	return nil
}
