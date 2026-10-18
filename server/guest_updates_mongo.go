package main

import (
	"context"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func loadGuestUpdateState(ctx context.Context, db *mongo.Database) (*guestUpdateState, error) {
	cur, err := db.Collection(guestUpdateCollection).Find(ctx, bson.D{}, options.Find().SetLimit(2))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var state *guestUpdateState
	for cur.Next(ctx) {
		if state != nil {
			return nil, errors.New("duplicate guest update singleton")
		}
		r := cur.Current
		if err := strictBSONKeys(r, "_id", "payload"); err != nil {
			return nil, err
		}
		id, ok := r.Lookup("_id").StringValueOK()
		if !ok || id != "singleton" {
			return nil, errors.New("invalid guest update singleton")
		}
		payload, ok := r.Lookup("payload").StringValueOK()
		if !ok {
			return nil, errors.New("invalid guest update payload")
		}
		state = &guestUpdateState{}
		if err := json.Unmarshal([]byte(payload), state); err != nil {
			return nil, err
		}
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	// Never permit a timer or custom uniqueness policy on the reservation ledger.
	indexes, err := db.Collection(guestUpdateCollection).Indexes().List(ctx)
	if err != nil {
		var ce mongo.CommandError
		if errors.As(err, &ce) && ce.Code == 26 {
			return state, nil
		}
		return nil, err
	}
	defer indexes.Close(ctx)
	for indexes.Next(ctx) {
		r := indexes.Current
		if err := strictBSONKeys(r, "v", "key", "name", "ns"); err != nil {
			return nil, err
		}
		key, ok := r.Lookup("key").DocumentOK()
		if !ok {
			return nil, errors.New("invalid guest update index")
		}
		els, e := key.Elements()
		name, _ := r.Lookup("name").StringValueOK()
		dir, good := key.Lookup("_id").Int32OK()
		if e != nil || len(els) != 1 || name != "_id_" || !good || dir != 1 {
			return nil, errors.New("unsupported guest update index")
		}
	}
	return state, indexes.Err()
}

func preflightGuestUpdates(ctx context.Context, db *mongo.Database, d *persistData) error {
	old, err := loadGuestUpdateState(ctx, db)
	if err != nil {
		return err
	}
	return guestUpdateTransition(old, d.GuestUpdates)
}
func saveGuestUpdates(ctx context.Context, db *mongo.Database, d *persistData) error {
	if d.GuestUpdates == nil || !isDirty(d, guestUpdateCollection) {
		return nil
	}
	raw, err := json.Marshal(d.GuestUpdates)
	if err != nil {
		return err
	}
	_, err = db.Collection(guestUpdateCollection).ReplaceOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}, bson.D{{Key: "_id", Value: "singleton"}, {Key: "payload", Value: string(raw)}}, options.Replace().SetUpsert(true))
	return err
}
