package main

import (
	"context"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/growprotocol"
)

func loadGuestGrowState(ctx context.Context, db *mongo.Database) (*growprotocol.State, error) {
	cur, err := db.Collection(guestGrowCollection).Find(ctx, bson.D{}, options.Find().SetLimit(2))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var state *growprotocol.State
	for cur.Next(ctx) {
		if state != nil {
			return nil, errors.New("duplicate guest grow singleton")
		}
		r := cur.Current
		if err := strictBSONKeys(r, "_id", "payload"); err != nil {
			return nil, err
		}
		id, ok := r.Lookup("_id").StringValueOK()
		if !ok || id != "singleton" {
			return nil, errors.New("invalid guest grow singleton")
		}
		payload, ok := r.Lookup("payload").StringValueOK()
		if !ok {
			return nil, errors.New("invalid guest grow payload")
		}
		state = &growprotocol.State{}
		if err := json.Unmarshal([]byte(payload), state); err != nil {
			return nil, err
		}
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	// Never permit a timer or custom uniqueness policy on the reservation ledger.
	indexes, err := db.Collection(guestGrowCollection).Indexes().List(ctx)
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
			return nil, errors.New("invalid guest grow index")
		}
		els, e := key.Elements()
		name, _ := r.Lookup("name").StringValueOK()
		dir, good := key.Lookup("_id").Int32OK()
		if e != nil || len(els) != 1 || name != "_id_" || !good || dir != 1 {
			return nil, errors.New("unsupported guest grow index")
		}
	}
	if err := validateGuestGrows(state); err != nil {
		return nil, err
	}
	return state, indexes.Err()
}

func preflightGuestGrows(ctx context.Context, db *mongo.Database, d *persistData) error {
	if err := validateGuestGrows(d.GuestGrows); err != nil {
		return err
	}
	old, err := loadGuestGrowState(ctx, db)
	if err != nil {
		return err
	}
	return growprotocol.Transition(old, d.GuestGrows)
}
func saveGuestGrows(ctx context.Context, db *mongo.Database, d *persistData) error {
	if d.GuestGrows == nil || !isDirty(d, guestGrowCollection) {
		return nil
	}
	raw, err := json.Marshal(d.GuestGrows)
	if err != nil {
		return err
	}
	_, err = db.Collection(guestGrowCollection).ReplaceOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}, bson.D{{Key: "_id", Value: "singleton"}, {Key: "payload", Value: string(raw)}}, options.Replace().SetUpsert(true))
	return err
}
