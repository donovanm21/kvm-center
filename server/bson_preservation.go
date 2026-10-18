package main

import (
	"bytes"
	"errors"
	"reflect"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
)

// bsonFields deliberately follows BSON naming, not JSON naming. This adapter
// is restricted to the explicit host/VM model trees; schema constructs it does
// not understand are refused rather than treated as opaque user fields.
func bsonFields(t reflect.Type) (map[string]reflect.Type, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, errors.New("unsupported BSON schema")
	}
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag := strings.Split(f.Tag.Get("bson"), ",")
		if tag[0] == "-" {
			continue
		}
		for _, option := range tag[1:] {
			if option == "inline" {
				return nil, errors.New("unsupported inline BSON schema")
			}
		}
		name := tag[0]
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if _, exists := out[name]; exists {
			return nil, errors.New("duplicate BSON schema field")
		}
		out[name] = f.Type
	}
	return out, nil
}

func rawFields(raw bson.Raw) (map[string]bson.RawValue, error) {
	if err := raw.Validate(); err != nil {
		return nil, errors.New("invalid BSON document")
	}
	es, err := raw.Elements()
	if err != nil {
		return nil, err
	}
	out := map[string]bson.RawValue{}
	folded := map[string]bool{}
	for _, e := range es {
		key := e.Key()
		if folded[strings.ToLower(key)] {
			return nil, errors.New("ambiguous duplicate BSON field")
		}
		folded[strings.ToLower(key)] = true
		v := e.Value()
		if v.Type == bsontype.EmbeddedDocument || v.Type == bsontype.Array {
			if _, err := rawFields(bson.Raw(v.Value)); err != nil {
				return nil, err
			}
		}
		out[key] = v
	}
	return out, nil
}

func equalRaw(a, b bson.RawValue) bool { return a.Type == b.Type && bytes.Equal(a.Value, b.Value) }

// unknownBelow validates exact known-field representation while finding opaque
// descendants. No numeric coercion, null/absent collapse, or array identity
// inference is accepted. Whole containers with opaque descendants can only be
// retained when their typed projection is unchanged.
func unknownBelow(old, projected bson.RawValue, t reflect.Type) (bool, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if old.Type != projected.Type {
		return false, errors.New("unsupported BSON type conversion")
	}
	if old.Type == bsontype.EmbeddedDocument && t.Kind() == reflect.Map {
		if t.Key().Kind() != reflect.String || t.Elem().Kind() != reflect.String {
			return false, errors.New("unsupported BSON map schema")
		}
		a, err := rawFields(bson.Raw(old.Value))
		if err != nil {
			return false, err
		}
		b, err := rawFields(bson.Raw(projected.Value))
		if err != nil {
			return false, err
		}
		if len(a) != len(b) {
			return false, errors.New("BSON string map projection changed")
		}
		for key, value := range a {
			if value.Type != bsontype.String || !equalRaw(value, b[key]) {
				return false, errors.New("BSON string map value changed type or value")
			}
		}
		return false, nil
	}
	if old.Type == bsontype.EmbeddedDocument {
		fields, err := bsonFields(t)
		if err != nil {
			return false, err
		}
		a, err := rawFields(bson.Raw(old.Value))
		if err != nil {
			return false, err
		}
		b, err := rawFields(bson.Raw(projected.Value))
		if err != nil {
			return false, err
		}
		unknown := false
		for key, v := range a {
			ft, known := fields[key]
			if !known {
				for name := range fields {
					if strings.EqualFold(name, key) {
						return false, errors.New("noncanonical BSON field spelling")
					}
				}
				unknown = true
				continue
			}
			if b[key].Type == 0 {
				return false, errors.New("unsupported omitted BSON value")
			}
			u, err := unknownBelow(v, b[key], ft)
			if err != nil {
				return false, err
			}
			unknown = unknown || u
		}
		return unknown, nil
	}
	if old.Type == bsontype.Array {
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return false, errors.New("unsupported BSON array schema")
		}
		a, err := bson.Raw(old.Value).Values()
		if err != nil {
			return false, err
		}
		b, err := bson.Raw(projected.Value).Values()
		if err != nil {
			return false, err
		}
		if len(a) != len(b) {
			return false, errors.New("unsupported BSON array projection")
		}
		unknown := false
		for i := range a {
			u, err := unknownBelow(a[i], b[i], t.Elem())
			if err != nil {
				return false, err
			}
			unknown = unknown || u
		}
		return unknown, nil
	}
	if !equalRaw(old, projected) {
		return false, errors.New("unsupported BSON value conversion")
	}
	return false, nil
}

// preserveEntityBSON keeps opaque root fields and unchanged opaque containers
// for a retained identity. It does not claim their behavior is supported.
// Removing/changing a container containing unknown descendants is refused.
func preserveEntityBSON(old, candidate bson.Raw, entity reflect.Type, aliases map[string]string) (bson.Raw, error) {
	a, err := rawFields(old)
	if err != nil {
		return nil, err
	}
	b, err := rawFields(candidate)
	if err != nil {
		return nil, err
	}
	if !equalRaw(a["_id"], b["_id"]) || a["_id"].Type != bsontype.String {
		return nil, errors.New("BSON identity mismatch")
	}
	decoded := reflect.New(entity).Interface()
	if err := bson.Unmarshal(old, decoded); err != nil {
		return nil, err
	}
	projected, err := bson.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	p, err := rawFields(projected)
	if err != nil {
		return nil, err
	}
	fields, err := bsonFields(entity)
	if err != nil {
		return nil, err
	}
	retained := map[string]bson.RawValue{}
	for key, value := range a {
		if key == "_id" {
			continue
		}
		name := key
		if canonical, ok := aliases[key]; ok {
			name = canonical
		}
		ft, known := fields[name]
		if !known {
			for field := range fields {
				if strings.EqualFold(field, key) {
					return nil, errors.New("noncanonical BSON field spelling")
				}
			}
			if _, exists := b[key]; exists {
				return nil, errors.New("candidate changes an opaque BSON field")
			}
			retained[key] = value
			continue
		}
		if p[name].Type == 0 {
			return nil, errors.New("unsupported omitted BSON value")
		}
		u, err := unknownBelow(value, p[name], ft)
		if err != nil {
			return nil, err
		}
		// An unchanged string map keeps its original raw order. Re-marshalling
		// Go maps may reorder entries and break exact restore verification.
		if ft.Kind() == reflect.Map && ft.Key().Kind() == reflect.String && ft.Elem().Kind() == reflect.String && value.Type == bsontype.EmbeddedDocument {
			if _, same := unknownBelow(value, b[name], ft); same == nil {
				retained[name] = value
			}
		}
		if u {
			if !equalRaw(b[name], p[name]) {
				return nil, errors.New("ambiguous mutation of opaque BSON container")
			}
			retained[name] = value
		}
	}
	es, _ := candidate.Elements()
	out := bson.D{}
	for _, e := range es {
		v := e.Value()
		if keep, ok := retained[e.Key()]; ok {
			v = keep
			delete(retained, e.Key())
		}
		out = append(out, bson.E{Key: e.Key(), Value: v})
	}
	// Preserve original order for appended unknown fields, and raw value bytes.
	es, _ = old.Elements()
	for _, e := range es {
		if v, ok := retained[e.Key()]; ok {
			out = append(out, bson.E{Key: e.Key(), Value: v})
		}
	}
	return bson.Marshal(out)
}
