package main

import (
	"context"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/models"
	"reflect"
	"strings"
	"time"
)

// Entity overlays are private backup data, never public model/API fields.
// Version 1 supports exactly these five entity collections, not full legacy activation.
type entityOverlay struct {
	Version     int                          `json:"version"`
	Collections map[string]map[string][]byte `json:"collections"`
}
type entityOverlayWire entityOverlay

var overlayCollections = []string{mcoHosts, mcoVMs, mcoUsers, mcoGroups, mcoRoles}

func (o *entityOverlay) validate() error {
	if o == nil {
		return nil
	}
	if o.Version != 1 || o.Collections == nil || len(o.Collections) > 5 {
		return errors.New("unsupported entity overlay schema")
	}
	size := 0
	count := 0
	for col, records := range o.Collections {
		if _, _, err := entitySchema(col); err != nil {
			return err
		}
		if records == nil {
			return errors.New("null entity overlay collection")
		}
		for id, raw := range records {
			size += len(raw)
			count++
			if len(raw) > 1<<20 || size > 16<<20 || count > 10000 {
				return errors.New("entity overlay exceeds bounds")
			}
			got, err := entityIdentity(raw, col)
			if err != nil {
				return err
			}
			if id != got {
				return errors.New("entity overlay identity mismatch")
			}
		}
	}
	return nil
}
func (o *entityOverlay) UnmarshalJSON(raw []byte) error {
	if len(raw) > 24<<20 {
		return errors.New("entity overlay exceeds input bound")
	}
	if err := uniqueJSONKeys(raw); err != nil {
		return err
	}
	if _, err := historyObject(raw, "version", "collections"); err != nil {
		return err
	}
	var w entityOverlayWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	n := entityOverlay(w)
	if err := n.validate(); err != nil {
		return err
	}
	*o = n
	return nil
}

func overlayCandidates(d *persistData) map[string][]any {
	out := map[string][]any{}
	for _, name := range overlayCollections {
		out[name] = nil
	}
	out[mcoHosts] = mapToDocs(d.Hosts, func(v *models.Host) any {
		return struct {
			ID           string `bson:"_id"`
			*models.Host `bson:",inline"`
		}{v.ID, v}
	})
	for host, vs := range d.VMs {
		for _, v := range vs {
			id := strings.ToLower(v.UUID)
			if id == "" {
				id = host + "|" + v.Name
			}
			out[mcoVMs] = append(out[mcoVMs], struct {
				ID         string `bson:"_id"`
				*models.VM `bson:",inline"`
			}{id, v})
		}
	}
	out[mcoUsers] = mapToDocs(d.Users, func(v *models.User) any {
		return struct {
			ID           string `bson:"_id"`
			*models.User `bson:",inline"`
		}{v.ID, v}
	})
	out[mcoGroups] = mapToDocs(d.Groups, func(v *models.Group) any {
		return struct {
			ID            string `bson:"_id"`
			*models.Group `bson:",inline"`
		}{v.ID, v}
	})
	out[mcoRoles] = mapToDocs(d.Roles, func(v *models.Role) any {
		return struct {
			ID           string `bson:"_id"`
			*models.Role `bson:",inline"`
		}{v.ID, v}
	})
	return out
}

// Reconcile typed edits before export: reject incompatible opaque-container
// changes and prune deleted identities. Typed credential/permission fields win.
func reconcileOverlay(d *persistData, old *entityOverlay) (*entityOverlay, error) {
	if old == nil {
		return nil, nil
	}
	if err := validateEntitySnapshot(d); err != nil {
		return nil, err
	}
	if err := old.validate(); err != nil {
		return nil, err
	}
	next := &entityOverlay{Version: 1, Collections: map[string]map[string][]byte{}}
	for col, docs := range overlayCandidates(d) {
		typ, aliases, _ := entitySchema(col)
		records := map[string][]byte{}
		for _, doc := range docs {
			raw, err := bson.Marshal(doc)
			if err != nil {
				return nil, err
			}
			id, err := entityIdentity(raw, col)
			if err != nil {
				return nil, err
			}
			if _, ok := records[id]; ok {
				return nil, errors.New("duplicate overlay candidate identity")
			}
			if original, ok := old.Collections[col][id]; ok {
				raw, err = preserveEntityBSON(original, raw, typ, aliases)
				if err != nil {
					return nil, err
				}
			}
			records[id] = raw
		}
		next.Collections[col] = records
	}
	return next, next.validate()
}

func (b *MongoBackend) captureEntityOverlay(ctx context.Context, d *persistData) (*entityOverlay, error) {
	if err := validateEntitySnapshot(d); err != nil {
		return nil, err
	}
	db, err := b.dial(ctx)
	if err != nil {
		return nil, err
	}
	o := &entityOverlay{Version: 1, Collections: map[string]map[string][]byte{}}
	for col, docs := range overlayCandidates(d) {
		prepared, err := prepareEntityDocuments(ctx, db, col, docs)
		if err != nil {
			return nil, err
		}
		o.Collections[col] = map[string][]byte{}
		for _, doc := range prepared {
			raw := doc.(bson.Raw)
			id, err := entityIdentity(raw, col)
			if err != nil {
				return nil, err
			}
			o.Collections[col][id] = append([]byte(nil), raw...)
		}
	}
	return o, o.validate()
}

// Restore is explicit and fresh-destination-only. An error can leave a partial
// destination; discard that isolated destination and retry from the unchanged
// archive. It never authorizes ordinary Save to resurrect absent identities.
func (b *MongoBackend) restoreEntitySnapshot(ctx context.Context, d *persistData) error {
	overlay, err := reconcileOverlay(d, d.EntityOverlay)
	if err != nil {
		return err
	}
	if err := b.requireEmptyRestoreTarget(ctx); err != nil {
		return err
	}
	copy := *d
	copy.EntityOverlay = nil
	copy.restoreOverlay = overlay
	copy.Dirty = nil
	if err := b.Save(ctx, &copy); err != nil {
		return err
	}
	back, err := b.Load(ctx)
	if err != nil {
		return err
	}
	got, err := b.captureEntityOverlay(ctx, back)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, overlay) {
		return errors.New("entity overlay restore verification mismatch")
	}
	return nil
}

func restoreEntityRecords(d *persistData, col string) map[string][]byte {
	if d.restoreOverlay == nil {
		return nil
	}
	return d.restoreOverlay.Collections[col]
}

func validateEntitySnapshot(d *persistData) error {
	if d == nil {
		return errors.New("missing entity snapshot")
	}
	for _, name := range []string{"Hosts", "Users", "Groups", "Roles"} {
		values := reflect.ValueOf(d).Elem().FieldByName(name)
		iter := values.MapRange()
		for iter.Next() {
			v := iter.Value()
			if v.IsNil() || v.Elem().FieldByName("ID").String() != iter.Key().String() {
				return errors.New("null or mismatched snapshot entity")
			}
		}
	}
	for _, vs := range d.VMs {
		for _, v := range vs {
			if v == nil {
				return errors.New("null snapshot VM")
			}
		}
	}
	return nil
}

// Uses a separate connection with no index initialization, including when the
// ordinary backend has not yet been opened. This is read-only preflight.
func (b *MongoBackend) requireEmptyRestoreTarget(ctx context.Context) error {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(b.uri))
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = client.Disconnect(closeCtx)
	}()
	db := client.Database(b.dbName)
	names, err := db.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return err
	}
	for _, name := range names {
		n, err := db.Collection(name).CountDocuments(ctx, bson.D{})
		if err != nil {
			return err
		}
		if n != 0 {
			return errors.New("entity overlay restore requires an entirely empty destination")
		}
	}
	return nil
}
