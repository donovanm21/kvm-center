// Package offlinestaging preserves an offline BSON snapshot without connecting
// to MongoDB or hydrating application models. Verified does not mean activatable.
package offlinestaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
)

const maxDocument = 16 << 20

type mapping struct {
	canonical, alias string
	typ              bsontype.Type
	required         bool
}

var mappings = map[string]mapping{
	"vms":          {"host_id", "hostid", bsontype.String, true},
	"sessions":     {"expires_at", "expiresat", bsontype.DateTime, true},
	"migrations":   {"started_at", "startedat", bsontype.Int64, true},
	"audit_events": {"actor_id", "actorid", bsontype.String, false},
}

func validateRaw(raw bson.Raw, depth int, array bool) error {
	if depth > 32 || len(raw) > maxDocument || len(raw) < 5 {
		return errors.New("BSON size/depth refused")
	}
	if err := raw.Validate(); err != nil {
		return errors.New("invalid BSON")
	}
	es, err := raw.Elements()
	if err != nil {
		return errors.New("invalid BSON elements")
	}
	seen := map[string]bool{}
	for i, e := range es {
		k := e.Key()
		if seen[k] || (array && k != strconv.Itoa(i)) {
			return errors.New("duplicate key or noncanonical array")
		}
		seen[k] = true
		v := e.Value()
		switch v.Type {
		case bsontype.EmbeddedDocument, bsontype.Array:
			if err := validateRaw(bson.Raw(v.Value), depth+1, v.Type == bsontype.Array); err != nil {
				return err
			}
		case bsontype.Double, bsontype.String, bsontype.Binary, bsontype.ObjectID, bsontype.Boolean, bsontype.DateTime, bsontype.Null, bsontype.Int32, bsontype.Timestamp, bsontype.Int64, bsontype.Decimal128, bsontype.MinKey, bsontype.MaxKey:
		default:
			return errors.New("unsupported BSON type")
		}
	}
	return nil
}

func documents(data []byte) ([]bson.Raw, error) {
	var result []bson.Raw
	for len(data) > 0 {
		if len(data) < 4 {
			return nil, errors.New("truncated BSON framing")
		}
		n := int(binary.LittleEndian.Uint32(data))
		if n < 5 || n > maxDocument || n > len(data) {
			return nil, errors.New("invalid BSON frame length")
		}
		raw := bson.Raw(data[:n])
		if err := validateRaw(raw, 0, false); err != nil {
			return nil, err
		}
		result = append(result, raw)
		if len(result) > 100000 {
			return nil, errors.New("too many documents")
		}
		data = data[n:]
	}
	return result, nil
}

func normalize(collection string, raw bson.Raw) (bson.Raw, []string, error) {
	m, ok := mappings[collection]
	if !ok {
		return append(bson.Raw(nil), raw...), nil, nil
	}
	es, _ := raw.Elements()
	var canonical, alias bson.RawValue
	for _, e := range es {
		k := e.Key()
		if k != m.canonical && k != m.alias && (strings.EqualFold(k, m.canonical) || strings.EqualFold(k, m.alias)) {
			return nil, nil, errors.New("ambiguous alias spelling")
		}
		if k == m.canonical || k == m.alias {
			v := e.Value()
			if v.Type != m.typ {
				return nil, nil, errors.New("mapped field has wrong BSON type")
			}
			if k == m.canonical {
				canonical = v
			} else {
				alias = v
			}
		}
	}
	if canonical.Type == 0 && alias.Type == 0 && m.required {
		return nil, nil, errors.New("required mapped field absent")
	}
	if canonical.Type != 0 && alias.Type != 0 && !bytes.Equal(canonical.Value, alias.Value) {
		return nil, nil, errors.New("conflicting aliases")
	}
	chosen := canonical
	if chosen.Type == 0 {
		chosen = alias
	}
	if collection == "vms" && chosen.StringValue() == "" {
		return nil, nil, errors.New("empty host reference")
	}
	if collection == "migrations" && chosen.Int64() <= 0 {
		return nil, nil, errors.New("invalid migration timestamp")
	}
	if alias.Type == 0 {
		return append(bson.Raw(nil), raw...), nil, nil
	}
	out := make([]byte, 4)
	var changes []string
	for _, e := range es {
		if e.Key() != m.alias {
			out = append(out, e...)
			continue
		}
		if canonical.Type != 0 {
			changes = append(changes, "remove_equal_alias")
			continue
		}
		out = append(out, byte(e.Value().Type))
		out = append(out, m.canonical...)
		out = append(out, 0)
		out = append(out, e.Value().Value...)
		changes = append(changes, "rename_alias")
	}
	out = append(out, 0)
	binary.LittleEndian.PutUint32(out, uint32(len(out)))
	return bson.Raw(out), changes, nil
}

// Independently account for each element; no model decode or map round-trip.
func verifyElements(collection string, src, dst bson.Raw) error {
	se, _ := src.Elements()
	de, _ := dst.Elements()
	m, mapped := mappings[collection]
	canonical := src.Lookup(m.canonical).Type != 0
	j := 0
	for _, e := range se {
		key := e.Key()
		if mapped && key == m.alias {
			if canonical {
				continue
			}
			key = m.canonical
		}
		if j >= len(de) || de[j].Key() != key || de[j].Value().Type != e.Value().Type || !bytes.Equal(de[j].Value().Value, e.Value().Value) {
			return errors.New("element preservation mismatch")
		}
		j++
	}
	if j != len(de) {
		return errors.New("extra normalized element")
	}
	return nil
}

func identity(v bson.RawValue) (string, error) {
	switch v.Type {
	case bsontype.String:
		if v.StringValue() == "" {
			break
		}
		return "s:" + v.StringValue(), nil
	case bsontype.ObjectID:
		return "o:" + string(v.Value), nil
	}
	return "", errors.New("identity requires nonempty string or ObjectID")
}

func validateDocuments(all map[string][]bson.Raw) error {
	hosts := map[string]bool{}
	for _, h := range all["hosts"] {
		id, e := identity(h.Lookup("_id"))
		if e != nil {
			return e
		}
		hosts[id] = true
	}
	for name, docs := range all {
		seen := map[string]bool{}
		for _, d := range docs {
			id, e := identity(d.Lookup("_id"))
			if e != nil {
				return e
			}
			if seen[id] {
				return errors.New("duplicate document identity")
			}
			seen[id] = true
			if name == "vms" {
				v := d.Lookup("host_id")
				id, e := identity(v)
				if e != nil || !hosts[id] {
					return errors.New("unresolved VM host reference")
				}
			}
		}
	}
	return nil
}

// Only simple binary string/ObjectID unique keys are supported. This avoids
// pretending that BSON byte equality implements Mongo collation/numeric rules.
func validateIndexes(collection string, data []byte, docs []bson.Raw) error {
	// Target sparse unique constraints must hold even when source metadata omits
	// those indexes. Never silently deduplicate or approximate numeric equality.
	targetKey := map[string]string{"vms": "uuid", "users": "username"}[collection]
	if targetKey != "" {
		seen := map[string]bool{}
		for _, doc := range docs {
			v := doc.Lookup(targetKey)
			if v.Type == 0 {
				continue
			}
			id, err := identity(v)
			if err != nil {
				return errors.New("unsupported target unique value")
			}
			if seen[id] {
				return errors.New("target unique collision")
			}
			seen[id] = true
		}
	}

	indexes, err := documents(data)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	keys := map[string]bool{}
	for _, idx := range indexes {
		es, _ := idx.Elements()
		for _, e := range es {
			switch e.Key() {
			case "name", "key", "v", "unique", "sparse", "expireAfterSeconds":
			default:
				return errors.New("unsupported index option")
			}
		}
		name, ok := idx.Lookup("name").StringValueOK()
		if !ok || name == "" || names[name] {
			return errors.New("invalid/duplicate index name")
		}
		names[name] = true
		if v := idx.Lookup("v"); v.Type != 0 && (v.Type != bsontype.Int32 || v.Int32() != 2) {
			return errors.New("unsupported index version")
		}
		kv := idx.Lookup("key")
		if kv.Type != bsontype.EmbeddedDocument {
			return errors.New("invalid index key")
		}
		ke, _ := bson.Raw(kv.Value).Elements()
		if len(ke) != 1 {
			return errors.New("compound indexes require a separate proven contract")
		}
		key := ke[0].Key()
		direction := ke[0].Value()
		if direction.Type != bsontype.Int32 || (direction.Int32() != 1 && direction.Int32() != -1) || strings.Contains(key, ".") {
			return errors.New("unsupported index direction/path")
		}
		signature := key + ":" + fmt.Sprint(direction.Int32())
		if keys[signature] {
			return errors.New("duplicate index key pattern")
		}
		keys[signature] = true
		if m, ok := mappings[collection]; ok && strings.EqualFold(key, m.alias) {
			return errors.New("legacy alias index requires explicit resolution")
		}
		flag := func(k string) (bool, error) {
			v := idx.Lookup(k)
			if v.Type == 0 {
				return false, nil
			}
			if v.Type != bsontype.Boolean {
				return false, errors.New("invalid index flag")
			}
			return v.Boolean(), nil
		}
		unique, err := flag("unique")
		if err != nil {
			return err
		}
		sparse, err := flag("sparse")
		if err != nil {
			return err
		}
		if key == "_id" {
			if direction.Int32() != 1 || sparse {
				return errors.New("incompatible identity index")
			}
			unique = true
		}
		ttl := idx.Lookup("expireAfterSeconds")
		ttlSeconds := int64(-1)
		if ttl.Type != 0 {
			switch ttl.Type {
			case bsontype.Int32:
				ttlSeconds = int64(ttl.Int32())
			case bsontype.Int64:
				ttlSeconds = ttl.Int64()
			default:
				return errors.New("invalid TTL type")
			}
			if ttlSeconds < 0 || unique || sparse {
				return errors.New("unsupported TTL index")
			}
		}
		// Existing indexes matching application's required fields must have exactly
		// the expected options. Missing indexes are never created by this utility.
		expected := map[string]struct {
			dir            int32
			unique, sparse bool
			ttl            int64
		}{
			"vms/uuid": {1, true, true, -1}, "vms/host_id": {1, false, false, -1}, "users/username": {1, true, true, -1},
			"sessions/expires_at": {1, false, false, 0}, "migrations/started_at": {-1, false, false, -1}, "migrations/expires_at": {1, false, false, 0},
			"audit_events/actor_id": {1, false, false, -1}, "audit_events/action": {1, false, false, -1}, "guest_data/updated_at": {1, false, false, 300},
			"command_log/submitted_at": {-1, false, false, -1}, "command_log/outcome": {1, false, false, -1}, "command_log/expires_at": {1, false, false, 0},
		}
		if x, ok := expected[collection+"/"+key]; ok && (direction.Int32() != x.dir || unique != x.unique || sparse != x.sparse || ttlSeconds != x.ttl) {
			return errors.New("incompatible target index")
		}
		if collection == "audit_events" && key == "at" {
			if unique || sparse || !((direction.Int32() == -1 && ttlSeconds == -1) || (direction.Int32() == 1 && ttlSeconds == 31536000)) {
				return errors.New("incompatible audit index")
			}
		}
		seen := map[string]bool{}
		for _, doc := range docs {
			v := doc.Lookup(key)
			if ttl.Type != 0 && v.Type != 0 && v.Type != bsontype.DateTime {
				return errors.New("TTL value must be a date")
			}
			if !unique {
				continue
			}
			if v.Type == 0 && sparse {
				continue
			}
			id, err := identity(v)
			if err != nil {
				return errors.New("unsupported unique index value (including null/missing)")
			}
			if seen[id] {
				return errors.New("unique index collision")
			}
			seen[id] = true
		}
	}
	return nil
}
