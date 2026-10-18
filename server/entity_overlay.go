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
// Version 1 supports five startup collections; version 2 adds storage and notes.
// Version 3 adds retirement records; version 4 adds cached guest reports.
// None establishes full legacy activation or authorizes TTL expiry of originals.
type entityOverlay struct {
	Version     int                          `json:"version"`
	Collections map[string]map[string][]byte `json:"collections"`
}
type entityOverlayWire entityOverlay

type entityVMNote struct {
	UUID string `bson:"vm_uuid"`
	Text string `bson:"notes"`
}

var overlayCollections = []string{mcoHosts, mcoVMs, mcoUsers, mcoGroups, mcoRoles, mcoStorage, mcoVMNotes, mcoRemovedHosts, mcoGuestData}

func (o *entityOverlay) validate() error {
	if o == nil {
		return nil
	}
	if (o.Version < 1 || o.Version > 4) || o.Collections == nil || len(o.Collections) > len(overlayCollections) {
		return errors.New("unsupported entity overlay schema")
	}
	size := 0
	count := 0
	for col, records := range o.Collections {
		if o.Version < 4 && col == mcoGuestData {
			return errors.New("guest reports require entity overlay version 4")
		}
		if o.Version < 3 && col == mcoRemovedHosts {
			return errors.New("retirement requires entity overlay version 3")
		}
		if o.Version == 1 && (col == mcoStorage || col == mcoVMNotes) {
			return errors.New("collection requires entity overlay version 2")
		}
		if _, _, err := entitySchema(col); err != nil {
			return err
		}
		if records == nil {
			return errors.New("null entity overlay collection")
		}
		retiredIDs := map[string]bool{}
		for id, raw := range records {
			if col == mcoRemovedHosts {
				low := strings.ToLower(id)
				if retiredIDs[low] {
					return errors.New("case-colliding retirement overlay")
				}
				retiredIDs[low] = true
			}
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
	for host, pools := range d.Storage {
		for _, pool := range pools {
			out[mcoStorage] = append(out[mcoStorage], struct {
				ID                  string `bson:"_id"`
				*models.StoragePool `bson:",inline"`
			}{host + "|" + pool.Name, pool})
		}
	}
	for uuid, notes := range d.VMNotes {
		out[mcoVMNotes] = append(out[mcoVMNotes], struct {
			ID   string       `bson:"_id"`
			Note entityVMNote `bson:",inline"`
		}{uuid, entityVMNote{uuid, notes}})
	}
	out[mcoRemovedHosts] = mapToDocs(d.RemovedHosts, func(v *models.RemovedHost) any {
		return struct {
			ID                  string `bson:"_id"`
			*models.RemovedHost `bson:",inline"`
		}{v.HostID, v}
	})
	for uuid, entry := range d.GuestData {
		out[mcoGuestData] = append(out[mcoGuestData], struct {
			ID        string      `bson:"_id"`
			UUID      string      `bson:"vm_uuid"`
			UpdatedAt time.Time   `bson:"updated_at"`
			Entry     *guestEntry `bson:"entry"`
		}{uuid, uuid, entry.UpdatedAt, entry})
	}
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
	next := &entityOverlay{Version: 4, Collections: map[string]map[string][]byte{}}
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
	o := &entityOverlay{Version: 4, Collections: map[string]map[string][]byte{}}
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
	for host, pools := range d.Storage {
		seen := map[string]bool{}
		for _, pool := range pools {
			if pool == nil || pool.HostID != host || host == "" || pool.Name == "" || strings.Contains(host, "|") || strings.Contains(pool.Name, "|") || seen[pool.Name] {
				return errors.New("null, duplicate or ambiguous storage identity")
			}
			seen[pool.Name] = true
		}
	}
	for key, record := range d.RemovedHosts {
		if record == nil || record.HostID == "" || strings.TrimSpace(record.HostID) != record.HostID || key != strings.ToLower(record.HostID) {
			return errors.New("null or mismatched retirement snapshot identity")
		}
	}
	for uuid, entry := range d.GuestData {
		if entry == nil || uuid == "" || uuid != strings.ToLower(strings.TrimSpace(uuid)) || strings.ToLower(entry.Payload.VMUUID) != uuid || strings.TrimSpace(entry.Payload.VMUUID) != entry.Payload.VMUUID {
			return errors.New("null or mismatched guest snapshot identity")
		}
	}
	for uuid := range d.VMNotes {
		if uuid == "" || uuid != strings.ToLower(strings.TrimSpace(uuid)) {
			return errors.New("ambiguous snapshot VM note identity")
		}
	}
	for _, vs := range d.VMs {
		for _, v := range vs {
			if v == nil {
				return errors.New("null snapshot VM")
			}
			if note, ok := d.VMNotes[strings.ToLower(v.UUID)]; ok && v.Notes != "" && note != v.Notes {
				return errors.New("conflicting VM note representations")
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
