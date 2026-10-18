package main

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"kvm-center/internal/models"
)

func entityIdentity(raw bson.Raw, collection string) (string, error) {
	if _, err := rawFields(raw); err != nil {
		return "", err
	}
	id, ok := raw.Lookup("_id").StringValueOK()
	if !ok || id == "" {
		return "", errors.New("missing entity identity")
	}
	switch collection {
	case mcoHosts:
		var h models.Host
		if err := bson.Unmarshal(raw, &h); err != nil {
			return "", err
		}
		if h.ID != id {
			return "", errors.New("host identity mismatch")
		}
	case mcoVMs:
		var v models.VM
		if err := bson.Unmarshal(raw, &v); err != nil {
			return "", err
		}
		want := strings.ToLower(v.UUID)
		if want == "" {
			want = v.HostID + "|" + v.Name
		}
		if want != id {
			return "", errors.New("VM identity mismatch")
		}
	default:
		return "", errors.New("unsupported entity collection")
	}
	return id, nil
}

// prepareEntityDocuments completes compatibility preflight before either host
// or VM collection is written. Unknown semantics remain opaque, not supported
// behavior. Like the existing snapshot replacement contract, this requires a
// single application writer: it is not a transaction/CAS against other clients.
func prepareEntityDocuments(ctx context.Context, db *mongo.Database, collection string, docs []any) ([]any, error) {
	t := reflect.TypeOf(models.Host{})
	aliases := map[string]string{}
	if collection == mcoVMs {
		t = reflect.TypeOf(models.VM{})
		aliases["hostid"] = "host_id"
	}
	cur, err := db.Collection(collection).Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	existing := map[string]bson.Raw{}
	for cur.Next(ctx) {
		raw := append(bson.Raw(nil), cur.Current...)
		id, err := entityIdentity(raw, collection)
		if err != nil {
			return nil, err
		}
		existing[id] = raw
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	out := make([]any, 0, len(docs))
	seen := map[string]bool{}
	for _, doc := range docs {
		raw, err := bson.Marshal(doc)
		if err != nil {
			return nil, err
		}
		id, err := entityIdentity(raw, collection)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, errors.New("duplicate entity identity")
		}
		seen[id] = true
		if old, ok := existing[id]; ok {
			raw, err = preserveEntityBSON(old, raw, t, aliases)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, bson.Raw(raw))
	}
	return out, nil
}
