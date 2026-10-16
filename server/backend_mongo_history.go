package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const mcoHostHistory = "host_history_state"

// Never infer a destructive conversion from the independent local history format.
func loadHostHistory(ctx context.Context, db *mongo.Database) (*historyState, error) {
	err := db.Collection("perf_history").FindOne(ctx, bson.D{}, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Err()
	if err == nil {
		return nil, errors.New("legacy perf_history collection requires explicit offline conversion")
	}
	if err != mongo.ErrNoDocuments {
		return nil, err
	}
	cur, err := db.Collection(mcoHostHistory).Find(ctx, bson.D{}, options.Find().SetLimit(2))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var state *historyState
	for cur.Next(ctx) {
		if state != nil {
			return nil, errors.New("host history state must contain exactly one singleton")
		}
		raw := cur.Current
		if err := strictBSONKeys(raw, "_id", "state"); err != nil {
			return nil, err
		}
		id, ok := raw.Lookup("_id").StringValueOK()
		if !ok || id != "singleton" {
			return nil, errors.New("invalid host history state singleton identity")
		}
		payload := raw.Lookup("state")
		if payload.Type != bson.TypeEmbeddedDocument {
			return nil, errors.New("host history state requires a versioned object")
		}
		var next historyState
		if err := bson.Unmarshal(payload.Document(), &next); err != nil {
			return nil, fmt.Errorf("host history state load: %w", err)
		}
		state = &next
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return state, nil
}
func saveHostHistory(ctx context.Context, db *mongo.Database, d *persistData) error {
	if !isDirty(d, mcoHostHistory) {
		return nil
	}
	coll := db.Collection(mcoHostHistory)
	if d.HostHistory == nil || len(d.HostHistory.Hosts) == 0 {
		_, err := coll.DeleteMany(ctx, bson.D{})
		return err
	}
	_, err := coll.ReplaceOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}, bson.M{"_id": "singleton", "state": d.HostHistory}, options.Replace().SetUpsert(true))
	return err
}
