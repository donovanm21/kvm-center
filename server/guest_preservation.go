package main

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"reflect"
	"strings"
	"time"
)

// This is the exact nested writer layout, not the old loader's flattened projection.
type entityGuestReport struct {
	UUID      string     `bson:"vm_uuid"`
	UpdatedAt time.Time  `bson:"updated_at"`
	Entry     guestEntry `bson:"entry"`
}

func validateGuestRecord(raw bson.Raw) error {
	fields, err := rawFields(raw)
	if err != nil {
		return err
	}
	entry, ok := raw.Lookup("entry").DocumentOK()
	if !ok {
		return errors.New("missing guest entry document")
	}
	payload, ok := entry.Lookup("payload").DocumentOK()
	if !ok {
		return errors.New("missing guest payload document")
	}
	if raw.Lookup("updated_at").Type != 9 || entry.Lookup("updatedat").Type != 9 {
		return errors.New("missing guest update timestamps")
	}
	// JSON spellings are not additional BSON values. Refuse rather than choose
	// one representation or retain a conflicting alias as an opaque field.
	for _, pair := range []struct {
		raw     bson.Raw
		aliases []string
	}{
		{raw, []string{"vmuuid", "payload", "updatedat"}},
		{entry, []string{"updated_at"}},
		{payload, []string{"vm_uuid", "grow_result", "cpu_count", "mem_total_mb", "mem_used_mb", "kvm_tools_version"}},
	} {
		for _, alias := range pair.aliases {
			es, _ := pair.raw.Elements()
			for _, e := range es {
				if strings.EqualFold(alias, e.Key()) {
					return errors.New("unsupported guest BSON alias")
				}
			}
		}
	}
	if err := rejectGuestJSONAliases(raw, reflect.TypeOf(entityGuestReport{})); err != nil {
		return err
	}
	var doc entityGuestReport
	if err := bson.Unmarshal(raw, &doc); err != nil {
		return err
	}
	id, ok := fields["_id"].StringValueOK()
	if !ok || id == "" || id != strings.ToLower(strings.TrimSpace(id)) || doc.UUID != id || strings.ToLower(doc.Entry.Payload.VMUUID) != id || strings.TrimSpace(doc.Entry.Payload.VMUUID) != doc.Entry.Payload.VMUUID {
		return errors.New("ambiguous guest identity")
	}
	if !doc.UpdatedAt.Equal(doc.Entry.UpdatedAt) {
		return errors.New("conflicting guest update timestamps")
	}
	projection, err := bson.Marshal(doc)
	if err != nil {
		return err
	}
	// Validate every known type and spelling without accepting lossy coercion.
	_, err = unknownBelow(bson.RawValue{Type: 3, Value: raw}, bson.RawValue{Type: 3, Value: projection}, reflect.TypeOf(doc))
	return err
}

func preflightCollections(ctx context.Context, db *mongo.Database) error {
	if _, err := loadAuditRecords(ctx, db); err != nil {
		return err
	}
	if err := rejectLegacyCollections(ctx, db); err != nil {
		return err
	}
	cur, err := db.Collection(mcoGuestData).Find(ctx, bson.D{})
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		if err := validateGuestRecord(cur.Current); err != nil {
			return err
		}
	}
	return cur.Err()
}

// Reject cross-format spellings anywhere in the declared guest tree, even if
// the canonical BSON field is absent. Unknown non-alias fields stay opaque.
func rejectGuestJSONAliases(raw bson.Raw, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	elements, err := raw.Elements()
	if err != nil {
		return err
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		canonical := strings.Split(field.Tag.Get("bson"), ",")[0]
		if canonical == "" {
			canonical = strings.ToLower(field.Name)
		}
		alias := strings.Split(field.Tag.Get("json"), ",")[0]
		for _, element := range elements {
			if alias != "" && alias != "-" && alias != canonical && strings.EqualFold(element.Key(), alias) {
				return errors.New("unsupported guest JSON field in BSON")
			}
			if element.Key() != canonical {
				continue
			}
			value := element.Value()
			ft := field.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if value.Type == 3 {
				if err := rejectGuestJSONAliases(value.Document(), ft); err != nil {
					return err
				}
			}
			if value.Type == 4 && ft.Kind() == reflect.Slice {
				values, err := value.Array().Values()
				if err != nil {
					return err
				}
				for _, v := range values {
					if v.Type == 3 {
						if err := rejectGuestJSONAliases(v.Document(), ft.Elem()); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

// Caller holds s.mu: stage and validate before changing the report, VM view or
// pending instruction queues. This is single-writer compatibility preflight,
// not a durability acknowledgement or a transaction with other DB writers.
func (s *Store) preflightGuestUpdateLocked(id string, entry *guestEntry) error {
	d := &persistData{GuestData: map[string]*guestEntry{id: entry}}
	if err := validateEntitySnapshot(d); err != nil {
		return err
	}
	candidate := overlayCandidates(d)[mcoGuestData]
	if b, ok := s.backend.(*MongoBackend); ok {
		if !s.saveGateOpen {
			return errors.New("guest persistence unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		db, err := b.dial(ctx)
		if err != nil {
			return err
		}
		var old bson.Raw
		err = db.Collection(mcoGuestData).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&old)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := validateGuestRecord(old); err != nil {
			return err
		}
		raw, err := bson.Marshal(candidate[0])
		if err != nil {
			return err
		}
		_, err = preserveEntityBSON(old, raw, reflect.TypeOf(entityGuestReport{}), nil)
		return err
	}
	if s.entityOverlay != nil {
		if err := s.entityOverlay.validate(); err != nil {
			return err
		}
		if old, ok := s.entityOverlay.Collections[mcoGuestData][id]; ok {
			raw, err := bson.Marshal(candidate[0])
			if err != nil {
				return err
			}
			_, err = preserveEntityBSON(old, raw, reflect.TypeOf(entityGuestReport{}), nil)
			return err
		}
	}
	return nil
}
