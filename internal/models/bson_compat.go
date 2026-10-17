package models

import (
	"bytes"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
)

// canonicalBSONField is a decoding adapter, not an offline data converter.
// It never mutates the input. The model decoder still ignores unknown fields;
// preserving a complete older schema requires its original raw BSON snapshot.
func canonicalBSONField(data []byte, canonical, alias string, want bsontype.Type) ([]byte, error) {
	raw := bson.Raw(data)
	if err := raw.Validate(); err != nil {
		return nil, fmt.Errorf("invalid BSON document")
	}
	elements, err := raw.Elements()
	if err != nil {
		return nil, fmt.Errorf("invalid BSON elements")
	}
	seen := map[string]bool{}
	var current, old bson.RawValue
	for _, element := range elements {
		key := element.Key()
		if seen[key] {
			return nil, fmt.Errorf("duplicate BSON field")
		}
		seen[key] = true
		if key != canonical && key != alias && (strings.EqualFold(key, canonical) || strings.EqualFold(key, alias)) {
			return nil, fmt.Errorf("noncanonical spelling of BSON field %s", canonical)
		}
		if key == canonical || key == alias {
			value := element.Value()
			if value.Type != want {
				return nil, fmt.Errorf("BSON field %s requires %s", canonical, want)
			}
			if key == canonical {
				current = value
			} else {
				old = value
			}
		}
	}
	if current.Type != 0 && old.Type != 0 && !bytes.Equal(current.Value, old.Value) {
		return nil, fmt.Errorf("conflicting BSON aliases for %s", canonical)
	}
	if old.Type == 0 {
		return data, nil
	}
	normalized := bson.D{}
	for _, element := range elements {
		key := element.Key()
		if key == alias {
			if current.Type != 0 {
				continue
			}
			key = canonical
		}
		normalized = append(normalized, bson.E{Key: key, Value: element.Value()})
	}
	return bson.Marshal(normalized)
}

// Missing fields keep their historical zero-value semantics. Present null,
// wrong types, duplicate keys and conflicting aliases are rejected atomically.
// Method-free aliases prevent recursive UnmarshalBSON calls.
func (v *VM) UnmarshalBSON(data []byte) error {
	raw, err := canonicalBSONField(data, "host_id", "hostid", bsontype.String)
	if err != nil {
		return err
	}
	type plain VM
	var next plain
	if err = bson.Unmarshal(raw, &next); err != nil {
		return err
	}
	*v = VM(next)
	return nil
}
func (v *Session) UnmarshalBSON(data []byte) error {
	raw, err := canonicalBSONField(data, "expires_at", "expiresat", bsontype.DateTime)
	if err != nil {
		return err
	}
	type plain Session
	var next plain
	if err = bson.Unmarshal(raw, &next); err != nil {
		return err
	}
	*v = Session(next)
	return nil
}
func (v *MigrationRecord) UnmarshalBSON(data []byte) error {
	raw, err := canonicalBSONField(data, "started_at", "startedat", bsontype.Int64)
	if err != nil {
		return err
	}
	type plain MigrationRecord
	var next plain
	if err = bson.Unmarshal(raw, &next); err != nil {
		return err
	}
	*v = MigrationRecord(next)
	return nil
}
func (v *AuditEvent) UnmarshalBSON(data []byte) error {
	raw, err := canonicalBSONField(data, "actor_id", "actorid", bsontype.String)
	if err != nil {
		return err
	}
	type plain AuditEvent
	var next plain
	if err = bson.Unmarshal(raw, &next); err != nil {
		return err
	}
	*v = AuditEvent(next)
	return nil
}
