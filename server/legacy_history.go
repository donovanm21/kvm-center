package main

import (
	"context"
	"errors"
	"reflect"
	"sort"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/offlinestaging"
)

type legacyHistoryState = offlinestaging.LegacyHistoryState

const mcoLegacyHistory = "perf_history"
const mcoLegacyHistoryContract = "legacy_history_contract"

// The marker binds the exact complete immutable record set, including BSON
// ordering and types. It is a consistency contract, not deployment provenance.
func legacyHistoryMarker(s *legacyHistoryState) (bson.D, error) {
	hash, err := s.Digest()
	if err != nil {
		return nil, err
	}
	return bson.D{{Key: "_id", Value: "singleton"}, {Key: "version", Value: int32(1)}, {Key: "source_contract", Value: offlinestaging.LegacyHistorySource}, {Key: "sha256", Value: hash}}, nil
}
func legacyHistoryIndexes(ctx context.Context, db *mongo.Database) error {
	for _, col := range []string{mcoLegacyHistory, mcoLegacyHistoryContract} {
		cur, err := db.Collection(col).Indexes().List(ctx)
		if err != nil {
			var command mongo.CommandError
			if errors.As(err, &command) && command.Code == 26 {
				continue
			}
			return err
		}
		for cur.Next(ctx) {
			raw := cur.Current
			if err := strictBSONKeys(raw, "v", "key", "name", "ns"); err != nil {
				cur.Close(ctx)
				return errors.New("unsupported legacy history index options")
			}
			key, ok := raw.Lookup("key").DocumentOK()
			if !ok {
				cur.Close(ctx)
				return errors.New("invalid legacy history index key")
			}
			els, _ := key.Elements()
			name, _ := raw.Lookup("name").StringValueOK()
			direction, dirOK := key.Lookup("_id").Int32OK()
			if !ok || name != "_id_" || len(els) != 1 || !dirOK || direction != 1 {
				cur.Close(ctx)
				return errors.New("unsupported legacy history index")
			}
		}
		err = cur.Err()
		cur.Close(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}
func loadLegacyHistory(ctx context.Context, db *mongo.Database) (*legacyHistoryState, error) {
	cur, err := db.Collection(mcoLegacyHistoryContract).Find(ctx, bson.D{}, options.Find().SetLimit(2))
	if err != nil {
		return nil, err
	}
	var marker bson.Raw
	for cur.Next(ctx) {
		if marker != nil {
			cur.Close(ctx)
			return nil, errors.New("duplicate legacy history contract")
		}
		marker = append(bson.Raw(nil), cur.Current...)
	}
	err = cur.Err()
	cur.Close(ctx)
	if err != nil {
		return nil, err
	}
	cur, err = db.Collection(mcoLegacyHistory).Find(ctx, bson.D{}, options.Find().SetLimit(offlinestaging.LegacyHistoryMaxHosts+1))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	state := &legacyHistoryState{Version: 1, SourceContract: offlinestaging.LegacyHistorySource, Records: map[string][]byte{}}
	total := 0
	for cur.Next(ctx) {
		if marker == nil {
			return nil, errors.New("legacy perf_history collection requires explicit offline conversion")
		}
		raw := cur.Current
		total += len(raw)
		id, ok := raw.Lookup("_id").StringValueOK()
		if !ok || state.Records[id] != nil || len(state.Records) >= offlinestaging.LegacyHistoryMaxHosts || total > offlinestaging.LegacyHistoryMaxBytes {
			return nil, errors.New("unsupported legacy history identity/size")
		}
		state.Records[id] = append([]byte(nil), raw...)
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	if marker == nil {
		return nil, nil
	}
	if err := strictBSONKeys(marker, "_id", "version", "source_contract", "sha256"); err != nil {
		return nil, err
	}
	id, idOK := marker.Lookup("_id").StringValueOK()
	version, vOK := marker.Lookup("version").Int32OK()
	source, sOK := marker.Lookup("source_contract").StringValueOK()
	hash, hOK := marker.Lookup("sha256").StringValueOK()
	if !idOK || id != "singleton" || !vOK || version != 1 || !sOK || source != offlinestaging.LegacyHistorySource || !hOK {
		return nil, errors.New("unsupported legacy history contract marker")
	}
	digest, err := state.Digest()
	if err != nil {
		return nil, err
	}
	if hash != digest {
		return nil, errors.New("legacy history contract hash mismatch")
	}
	if err := legacyHistoryIndexes(ctx, db); err != nil {
		return nil, err
	}
	return state, nil
}
func preflightLegacyHistorySave(ctx context.Context, db *mongo.Database, d *persistData, fresh bool) error {
	if err := d.LegacyHistory.Validate(); err != nil {
		return err
	}
	old, err := loadLegacyHistory(ctx, db)
	if err != nil {
		return err
	}
	if fresh && old == nil {
		if d.LegacyHistory != nil {
			return legacyHistoryIndexes(ctx, db)
		}
		return nil
	}
	if !reflect.DeepEqual(old, d.LegacyHistory) {
		return errors.New("immutable legacy history changed; explicit fresh-destination restore required")
	}
	return nil
}

// Called only after fresh-destination preflight; partial restore requires a new
// empty destination. The contract marker is written last, so partial copies fail.
func restoreLegacyHistoryRecords(ctx context.Context, db *mongo.Database, state *legacyHistoryState) error {
	if state == nil {
		return nil
	}
	if err := state.Validate(); err != nil {
		return err
	}
	ids := make([]string, 0, len(state.Records))
	for id := range state.Records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := db.Collection(mcoLegacyHistory).InsertOne(ctx, bson.Raw(state.Records[id])); err != nil {
			return err
		}
	}
	marker, err := legacyHistoryMarker(state)
	if err != nil {
		return err
	}
	_, err = db.Collection(mcoLegacyHistoryContract).InsertOne(ctx, marker)
	return err
}
