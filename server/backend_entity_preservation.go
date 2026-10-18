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
	case mcoGuestData:
		value = entityGuestReport{}
	case mcoSessions:
		value = models.Session{}
		aliases["expiresat"] = "expires_at"
	case mcoAPITokens:
		value = models.APIToken{}
	case mcoAuditEvents:
		value = models.AuditEvent{}
		aliases["actorid"] = "actor_id"
	case mcoRemovedHosts:
		value = models.RemovedHost{}
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
	case mcoGuestData:
		if err := validateGuestRecord(raw); err != nil {
			return "", err
		}
	case mcoVMNotes:
		var note entityVMNote
		if err := bson.Unmarshal(raw, &note); err != nil {
			return "", err
		}
		if note.UUID != id || id != strings.ToLower(strings.TrimSpace(id)) {
			return "", errors.New("ambiguous VM note identity")
		}
	case mcoRemovedHosts:
		fields, _ := raw.Elements()
		for _, field := range fields {
			if strings.EqualFold(field.Key(), "host_id") || strings.EqualFold(field.Key(), "removed_at") || strings.EqualFold(field.Key(), "removed_by") {
				return "", errors.New("unsupported retirement BSON alias")
			}
		}
		var retired models.RemovedHost
		if err := bson.Unmarshal(raw, &retired); err != nil {
			return "", err
		}
		if retired.HostID != id || strings.TrimSpace(id) != id {
			return "", errors.New("retirement identity mismatch")
		}
	case mcoSessions:
		if err := validateSessionBSON(raw); err != nil {
			return "", err
		}
	case mcoAPITokens:
		if err := validateTokenBSON(raw); err != nil {
			return "", err
		}
	case mcoAuditEvents:
		if err := validateAuditBSON(raw); err != nil {
			return "", err
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
	if collection == mcoSessions {
		if _, err := loadSessionRecords(ctx, db); err != nil {
			return nil, err
		}
	}
	if collection == mcoAPITokens {
		if _, err := loadTokenRecords(ctx, db); err != nil {
			return nil, err
		}
	}
	if collection == mcoAuditEvents {
		if _, err := loadAuditRecords(ctx, db); err != nil {
			return nil, err
		}
	}
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
	retiredIDs := map[string]string{}
	for id, raw := range source {
		if collection == mcoRemovedHosts {
			low := strings.ToLower(id)
			if prior, ok := retiredIDs[low]; ok && prior != id {
				return nil, errors.New("case-colliding retirement identities")
			}
			retiredIDs[low] = id
		}
		existing[id] = bson.Raw(raw)
	}
	for cur.Next(ctx) {
		raw := append(bson.Raw(nil), cur.Current...)
		id, err := entityIdentity(raw, collection)
		if err != nil {
			return nil, err
		}
		if collection == mcoRemovedHosts {
			low := strings.ToLower(id)
			if prior, ok := retiredIDs[low]; ok && prior != id {
				return nil, errors.New("case-colliding retirement identities")
			}
			retiredIDs[low] = id
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
		if collection == mcoRemovedHosts {
			if prior, ok := retiredIDs[strings.ToLower(id)]; ok && prior != id {
				return nil, errors.New("retirement spelling changed for retained identity")
			}
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
