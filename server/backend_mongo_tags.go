package main

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const mcoVMTagState = "vm_tag_state"

// Never infer a destructive conversion from the independent local tag format.
func loadVMTagState(ctx context.Context, db *mongo.Database) (*vmTagState, error) {
	err := db.Collection("vm_tags").FindOne(ctx, bson.D{}, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Err()
	if err == nil {
		return nil, errors.New("legacy vm_tags collection requires explicit offline conversion")
	}
	if err != mongo.ErrNoDocuments {
		return nil, err
	}
	cur, err := db.Collection(mcoVMTagState).Find(ctx, bson.D{}, options.Find().SetLimit(2))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var state *vmTagState
	for cur.Next(ctx) {
		if state != nil {
			return nil, errors.New("VM tag state must contain exactly one singleton")
		}
		raw := cur.Current
		if err := strictBSONKeys(raw, "_id", "state"); err != nil {
			return nil, err
		}
		id, ok := raw.Lookup("_id").StringValueOK()
		if !ok || id != "singleton" {
			return nil, errors.New("invalid VM tag state singleton identity")
		}
		payload := raw.Lookup("state")
		if payload.Type != bson.TypeEmbeddedDocument {
			return nil, errors.New("VM tag state requires a versioned object")
		}
		var next vmTagState
		if err := bson.Unmarshal(payload.Document(), &next); err != nil {
			return nil, fmt.Errorf("VM tag state load: %w", err)
		}
		state = &next
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return state, nil
}
func saveVMTagState(ctx context.Context, db *mongo.Database, d *persistData) error {
	if !isDirty(d, mcoVMTagState) {
		return nil
	}
	coll := db.Collection(mcoVMTagState)
	if d.VMTagState == nil || len(d.VMTagState.ByVM) == 0 {
		_, err := coll.DeleteMany(ctx, bson.D{})
		return err
	}
	_, err := coll.ReplaceOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}, bson.M{"_id": "singleton", "state": d.VMTagState}, options.Replace().SetUpsert(true))
	return err
}
