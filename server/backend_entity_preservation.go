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

func entitySchema(collection string) (reflect.Type, map[string]string, error) {
	aliases := map[string]string{}
	var value any
	switch collection {
	case mcoHosts:
		value = models.Host{}
	case mcoVMs:
		value = models.VM{}
		aliases["hostid"] = "host_id"
	case mcoUsers:
		value = models.User{}
	case mcoGroups:
		value = models.Group{}
	case mcoRoles:
		value = models.Role{}
	case mcoStorage:
		value = models.StoragePool{}
	case mcoVMNotes:
		value = entityVMNote{}
	default:
		return nil, nil, errors.New("unsupported entity collection")
	}
	return reflect.TypeOf(value), aliases, nil
}

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
	case mcoStorage:
		if raw.Lookup("host_id").Type != 0 {
			return "", errors.New("unsupported storage host_id alias")
		}
		var pool models.StoragePool
		if err := bson.Unmarshal(raw, &pool); err != nil {
			return "", err
		}
		if pool.HostID == "" || pool.Name == "" || strings.Contains(pool.HostID, "|") || strings.Contains(pool.Name, "|") || id != pool.HostID+"|"+pool.Name {
			return "", errors.New("ambiguous storage identity")
		}
	case mcoVMNotes:
		var note entityVMNote
		if err := bson.Unmarshal(raw, &note); err != nil {
			return "", err
		}
		if note.UUID != id || id != strings.ToLower(strings.TrimSpace(id)) {
			return "", errors.New("ambiguous VM note identity")
		}
	case mcoUsers, mcoGroups, mcoRoles:
		typ, _, err := entitySchema(collection)
		if err != nil {
			return "", err
		}
		model := reflect.New(typ)
		if err := bson.Unmarshal(raw, model.Interface()); err != nil {
			return "", err
		}
		if model.Elem().FieldByName("ID").String() != id {
			return "", errors.New("startup entity identity mismatch")
		}
	default:
		return "", errors.New("unsupported entity collection")
	}
	return id, nil
}

// prepareEntityDocuments completes compatibility preflight before supported
// entity collections are written. Unknown semantics remain opaque, not supported
// behavior. Like the existing snapshot replacement contract, this requires a
// single application writer: it is not a transaction/CAS against other clients.
func prepareEntityDocuments(ctx context.Context, db *mongo.Database, collection string, docs []any) ([]any, error) {
	return prepareEntityDocumentsFrom(ctx, db, collection, docs, nil)
}

func prepareEntityDocumentsFrom(ctx context.Context, db *mongo.Database, collection string, docs []any, source map[string][]byte) ([]any, error) {
	t, aliases, err := entitySchema(collection)
	if err != nil {
		return nil, err
	}
	cur, err := db.Collection(collection).Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	existing := map[string]bson.Raw{}
	for id, raw := range source {
		existing[id] = bson.Raw(raw)
	}
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
