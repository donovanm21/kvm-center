package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// MongoBackend is the scale-up persistence tier introduced in v5.1.
// Connection is lazy (first Load/Save dials) so a misconfigured URL
// doesn't kill server startup — the operator can still reach the UI
// via JSON backend fallback and fix things from Settings → Database.
//
// Save semantics mirror the JSON backend's "replace the whole world"
// model: drop-and-reinsert every collection inside a single logical
// operation. Cheap at current fleet sizes and keeps the conversion
// from persistData trivial. Per-collection dirty tracking is a v5.2
// optimisation if we ever need it.
type MongoBackend struct {
	uri    string
	dbName string

	mu     sync.Mutex
	client *mongo.Client
	db     *mongo.Database
}

// Mongo collection names. Declared as constants so grep-finding every
// persisted entity is one symbol search.
const (
	mcoHosts       = "hosts"
	mcoVMs         = "vms"         // flat per-VM documents keyed by uuid
	mcoStorage     = "storage"     // flat per-pool docs keyed by host_id+name
	mcoVMNotes     = "vm_notes"    // keyed by vm_uuid
	mcoGuestData   = "guest_data"  // keyed by vm_uuid, updated_at TTL
	mcoMigrations  = "migrations"  // append-only, indexed by started_at desc
	mcoUsers       = "users"       // username unique
	mcoGroups      = "groups"
	mcoRoles       = "roles"
	mcoSessions    = "sessions" // TTL on expires_at
	mcoIDPs        = "idps"
	mcoFeatures    = "features" // single doc with _id:"singleton"
	mcoServerMeta  = "server_meta" // _id:"singleton" — console_secret etc.
	mcoAuditEvents     = "audit_events"
	mcoSnapshotPolicies = "snapshot_policies"
	mcoNotifications    = "notifications" // singleton _id:"singleton"
	mcoAPITokens        = "api_tokens"    // keyed by token ID
)

func NewMongoBackend(uri, dbName string) *MongoBackend {
	if dbName == "" {
		dbName = "kvm-center"
	}
	return &MongoBackend{uri: uri, dbName: dbName}
}

func (b *MongoBackend) Name() string { return "mongo" }

// RedactedEndpoint strips the password out of the Mongo URI for UI
// display. Keeps host + username visible so the operator can tell
// which cluster they're pointed at without exposing credentials.
func (b *MongoBackend) RedactedEndpoint() string {
	u, err := url.Parse(b.uri)
	if err != nil {
		return "mongodb://(unparseable)"
	}
	if u.User != nil {
		if _, hasPass := u.User.Password(); hasPass {
			u.User = url.UserPassword(u.User.Username(), "****")
		}
	}
	return u.String()
}

func (b *MongoBackend) Info(ctx context.Context) BackendInfo {
	info := BackendInfo{Type: "mongo", Endpoint: b.RedactedEndpoint(), Healthy: false}
	db, err := b.dial(ctx)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	// Ping + dbStats give us both liveness and a size number for the UI.
	if err := db.Client().Ping(ctx, readpref.Primary()); err != nil {
		info.Error = "ping: " + err.Error()
		return info
	}
	var stats struct {
		DataSize int64 `bson:"dataSize"`
	}
	if err := db.RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}}).Decode(&stats); err == nil {
		info.SizeBytes = stats.DataSize
	}
	info.Healthy = true
	info.LastModified = time.Now().UTC().Format(time.RFC3339)
	return info
}

// dial lazy-connects and caches the client. Subsequent calls reuse it.
func (b *MongoBackend) dial(ctx context.Context) (*mongo.Database, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.db != nil {
		return b.db, nil
	}
	opts := options.Client().ApplyURI(b.uri).SetServerSelectionTimeout(8 * time.Second)
	cli, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	// Confirm reachability and auth before caching — a bad URL should
	// surface NOW, not on the first write ten minutes in.
	pingCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := cli.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = cli.Disconnect(context.Background())
		return nil, fmt.Errorf("mongo ping: %w", err)
	}
	b.client = cli
	b.db = cli.Database(b.dbName)
	if err := b.ensureIndexes(ctx); err != nil {
		return nil, fmt.Errorf("mongo indexes: %w", err)
	}
	return b.db, nil
}

func (b *MongoBackend) Close(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client == nil {
		return nil
	}
	err := b.client.Disconnect(ctx)
	b.client, b.db = nil, nil
	return err
}

// ensureIndexes runs every dial — CreateIndexes is idempotent on the
// Mongo side. Defined together so every index the app relies on lives
// in one place.
func (b *MongoBackend) ensureIndexes(ctx context.Context) error {
	type idx struct {
		col     string
		keys    bson.D
		options *options.IndexOptions
	}
	ensure := []idx{
		// Unique + sparse so legacy rows without a UUID (rare but
		// present in some pre-v4 exports) don't collide on null keys.
		{mcoVMs, bson.D{{Key: "uuid", Value: 1}}, options.Index().SetUnique(true).SetSparse(true)},
		{mcoVMs, bson.D{{Key: "host_id", Value: 1}}, nil},
		{mcoUsers, bson.D{{Key: "username", Value: 1}}, options.Index().SetUnique(true).SetSparse(true)},
		{mcoMigrations, bson.D{{Key: "started_at", Value: -1}}, nil},
		{mcoAuditEvents, bson.D{{Key: "at", Value: -1}}, nil},
		{mcoAuditEvents, bson.D{{Key: "actor_id", Value: 1}}, nil},
		{mcoAuditEvents, bson.D{{Key: "action", Value: 1}}, nil},
		{mcoSessions, bson.D{{Key: "expires_at", Value: 1}}, options.Index().SetExpireAfterSeconds(0)},
		// guest_data: drop docs older than 5 minutes to mirror the
		// guestToolsFreshness window the UI already uses.
		{mcoGuestData, bson.D{{Key: "updated_at", Value: 1}}, options.Index().SetExpireAfterSeconds(300)},
	}
	for _, i := range ensure {
		_, err := b.db.Collection(i.col).Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    i.keys,
			Options: i.options,
		})
		if err != nil {
			return fmt.Errorf("index %s %v: %w", i.col, i.keys, err)
		}
	}
	return nil
}

// Load reads every collection and reassembles a persistData. Shape
// matches the JSON backend's output exactly so the in-memory store
// doesn't know or care which backend produced it.
func (b *MongoBackend) Load(ctx context.Context) (*persistData, error) {
	db, err := b.dial(ctx)
	if err != nil {
		return nil, err
	}
	pd := &persistData{
		Hosts:     map[string]*models.Host{},
		VMs:       map[string][]*models.VM{},
		Storage:   map[string][]*models.StoragePool{},
		VMNotes:   map[string]string{},
		GuestData: map[string]*guestEntry{},
		Users:     map[string]*models.User{},
		Groups:    map[string]*models.Group{},
		Roles:     map[string]*models.Role{},
		Sessions:  map[string]*models.Session{},
		IDPs:      map[string]*models.IdentityProvider{},
	}

	// Hosts
	if err := loadMapByID[models.Host](ctx, db, mcoHosts, pd.Hosts, func(h *models.Host) string { return h.ID }); err != nil {
		return nil, err
	}

	// VMs — flat collection grouped back into map[host_id][]*VM
	cur, err := db.Collection(mcoVMs).Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var vms []*models.VM
	if err := cur.All(ctx, &vms); err != nil {
		return nil, err
	}
	for _, v := range vms {
		pd.VMs[v.HostID] = append(pd.VMs[v.HostID], v)
	}

	// Storage — same pattern
	cur, err = db.Collection(mcoStorage).Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var pools []*models.StoragePool
	if err := cur.All(ctx, &pools); err != nil {
		return nil, err
	}
	for _, p := range pools {
		pd.Storage[p.HostID] = append(pd.Storage[p.HostID], p)
	}

	// VM notes
	cur, err = db.Collection(mcoVMNotes).Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	for cur.Next(ctx) {
		var doc struct {
			UUID string `bson:"vm_uuid"`
			Text string `bson:"notes"`
		}
		if err := cur.Decode(&doc); err == nil && doc.UUID != "" {
			pd.VMNotes[doc.UUID] = doc.Text
		}
	}

	// Guest data
	cur, err = db.Collection(mcoGuestData).Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	for cur.Next(ctx) {
		var doc struct {
			UUID  string                            `bson:"vm_uuid"`
			Entry guestEntry                        `bson:"entry,inline"`
		}
		if err := cur.Decode(&doc); err == nil && doc.UUID != "" {
			e := doc.Entry
			pd.GuestData[doc.UUID] = &e
		}
	}

	// Migrations — load all, preserve order (started_at desc on read)
	opts := options.Find().SetSort(bson.D{{Key: "started_at", Value: -1}})
	cur, err = db.Collection(mcoMigrations).Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, err
	}
	if err := cur.All(ctx, &pd.Migrations); err != nil {
		return nil, err
	}

	// Users / Groups / Roles / IDPs
	if err := loadMapByID[models.User](ctx, db, mcoUsers, pd.Users, func(u *models.User) string { return u.ID }); err != nil {
		return nil, err
	}
	if err := loadMapByID[models.Group](ctx, db, mcoGroups, pd.Groups, func(g *models.Group) string { return g.ID }); err != nil {
		return nil, err
	}
	if err := loadMapByID[models.Role](ctx, db, mcoRoles, pd.Roles, func(r *models.Role) string { return r.ID }); err != nil {
		return nil, err
	}
	if err := loadMapByID[models.IdentityProvider](ctx, db, mcoIDPs, pd.IDPs, func(i *models.IdentityProvider) string { return i.ID }); err != nil {
		return nil, err
	}

	// Sessions (session token is the _id; TTL cleans expired)
	cur, err = db.Collection(mcoSessions).Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var sessions []*models.Session
	if err := cur.All(ctx, &sessions); err != nil {
		return nil, err
	}
	for _, s := range sessions {
		pd.Sessions[s.Token] = s
	}

	// Features singleton
	var feat models.Features
	err = db.Collection(mcoFeatures).FindOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}).Decode(&feat)
	if err == nil {
		pd.Features = &feat
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}

	// Server metadata singleton — currently just the console-token secret.
	var meta struct {
		ConsoleSecret string `bson:"console_secret"`
	}
	err = db.Collection(mcoServerMeta).FindOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}).Decode(&meta)
	if err == nil {
		pd.ConsoleSecret = meta.ConsoleSecret
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}

	// Audit events — loaded in chronological order so the ring-buffer
	// trim in Store behaves identically to the JSON backend path.
	auditOpts := options.Find().SetSort(bson.D{{Key: "at", Value: 1}})
	cur, err = db.Collection(mcoAuditEvents).Find(ctx, bson.D{}, auditOpts)
	if err != nil {
		return nil, err
	}
	if err := cur.All(ctx, &pd.Audit); err != nil {
		return nil, err
	}

	// Snapshot policies (v5.3) — keyed map, same pattern as users.
	pd.SnapshotPolicies = map[string]*models.SnapshotPolicy{}
	if err := loadMapByID[models.SnapshotPolicy](ctx, db, mcoSnapshotPolicies, pd.SnapshotPolicies, func(p *models.SnapshotPolicy) string { return p.ID }); err != nil {
		return nil, err
	}

	// API tokens (v5.5) keyed map.
	pd.APITokens = map[string]*models.APIToken{}
	if err := loadMapByID[models.APIToken](ctx, db, mcoAPITokens, pd.APITokens, func(t *models.APIToken) string { return t.ID }); err != nil {
		return nil, err
	}

	// Notifications (v5.4) singleton.
	var notif models.NotificationConfig
	err = db.Collection(mcoNotifications).FindOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}).Decode(&notif)
	if err == nil {
		pd.Notifications = &notif
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}

	return pd, nil
}

// Save replaces every collection's contents with the provided
// persistData snapshot. Uses drop-and-insert inside a single logical
// checkpoint: simple, correct, and — crucially — matches the JSON
// backend's "full-state persist" semantics so there are zero subtle
// differences between backends at the Store layer. Not the most
// network-efficient pattern; that's a v5.2 problem.
func (b *MongoBackend) Save(ctx context.Context, d *persistData) error {
	db, err := b.dial(ctx)
	if err != nil {
		return err
	}

	// Hosts. Every embed below uses bson:",inline" so the entity's
	// fields land at the document root rather than nested under the
	// Go struct's field name — otherwise the default decoder can't
	// hydrate the Host back from a "host" sub-object.
	if err := replaceMap(ctx, db, mcoHosts, mapToDocs(d.Hosts, func(h *models.Host) any {
		return struct {
			ID string `bson:"_id"`
			*models.Host `bson:",inline"`
		}{h.ID, h}
	})); err != nil {
		return err
	}

	// VMs — flatten nested map. Fall back to host|name for the _id when
	// the UUID field is empty (pre-v4 data) so docs don't collide.
	var vmDocs []any
	for hostID, vms := range d.VMs {
		for _, v := range vms {
			id := strings.ToLower(v.UUID)
			if id == "" {
				id = hostID + "|" + v.Name
			}
			vmDocs = append(vmDocs, struct {
				ID string `bson:"_id"`
				*models.VM `bson:",inline"`
			}{id, v})
		}
	}
	if err := replaceMap(ctx, db, mcoVMs, vmDocs); err != nil {
		return err
	}

	// Storage — same pattern
	var poolDocs []any
	for hostID, pools := range d.Storage {
		for _, p := range pools {
			poolDocs = append(poolDocs, struct {
				ID string `bson:"_id"`
				*models.StoragePool `bson:",inline"`
			}{hostID + "|" + p.Name, p})
		}
	}
	if err := replaceMap(ctx, db, mcoStorage, poolDocs); err != nil {
		return err
	}

	// VM notes
	var noteDocs []any
	for uuid, text := range d.VMNotes {
		noteDocs = append(noteDocs, bson.M{"_id": uuid, "vm_uuid": uuid, "notes": text})
	}
	if err := replaceMap(ctx, db, mcoVMNotes, noteDocs); err != nil {
		return err
	}

	// Guest data (updated_at drives the TTL index)
	var gdDocs []any
	for uuid, entry := range d.GuestData {
		gdDocs = append(gdDocs, bson.M{
			"_id":        uuid,
			"vm_uuid":    uuid,
			"updated_at": entry.UpdatedAt,
			"entry":      entry,
		})
	}
	if err := replaceMap(ctx, db, mcoGuestData, gdDocs); err != nil {
		return err
	}

	// Migrations — append-only list, persisted as the full array
	var migDocs []any
	for _, m := range d.Migrations {
		migDocs = append(migDocs, struct {
			ID string `bson:"_id"`
			*models.MigrationRecord `bson:",inline"`
		}{m.ID, m})
	}
	if err := replaceMap(ctx, db, mcoMigrations, migDocs); err != nil {
		return err
	}

	if err := replaceMap(ctx, db, mcoUsers, mapToDocs(d.Users, func(u *models.User) any {
		return struct {
			ID string `bson:"_id"`
			*models.User `bson:",inline"`
		}{u.ID, u}
	})); err != nil {
		return err
	}
	if err := replaceMap(ctx, db, mcoGroups, mapToDocs(d.Groups, func(g *models.Group) any {
		return struct {
			ID string `bson:"_id"`
			*models.Group `bson:",inline"`
		}{g.ID, g}
	})); err != nil {
		return err
	}
	if err := replaceMap(ctx, db, mcoRoles, mapToDocs(d.Roles, func(r *models.Role) any {
		return struct {
			ID string `bson:"_id"`
			*models.Role `bson:",inline"`
		}{r.ID, r}
	})); err != nil {
		return err
	}
	if err := replaceMap(ctx, db, mcoIDPs, mapToDocs(d.IDPs, func(i *models.IdentityProvider) any {
		return struct {
			ID string `bson:"_id"`
			*models.IdentityProvider `bson:",inline"`
		}{i.ID, i}
	})); err != nil {
		return err
	}

	// Sessions — keyed by token; TTL index auto-expires.
	var sessDocs []any
	for tok, s := range d.Sessions {
		sessDocs = append(sessDocs, struct {
			ID string `bson:"_id"`
			*models.Session `bson:",inline"`
		}{tok, s})
	}
	if err := replaceMap(ctx, db, mcoSessions, sessDocs); err != nil {
		return err
	}

	// Features singleton upsert
	if d.Features != nil {
		if _, err := db.Collection(mcoFeatures).ReplaceOne(
			ctx,
			bson.D{{Key: "_id", Value: "singleton"}},
			struct {
				ID string `bson:"_id"`
				*models.Features `bson:",inline"`
			}{"singleton", d.Features},
			options.Replace().SetUpsert(true),
		); err != nil {
			return err
		}
	}

	// Server metadata singleton
	if _, err := db.Collection(mcoServerMeta).ReplaceOne(
		ctx,
		bson.D{{Key: "_id", Value: "singleton"}},
		bson.M{"_id": "singleton", "console_secret": d.ConsoleSecret},
		options.Replace().SetUpsert(true),
	); err != nil {
		return err
	}

	// Audit events — replace-all like every other collection.
	var auditDocs []any
	for _, ev := range d.Audit {
		auditDocs = append(auditDocs, struct {
			ID string `bson:"_id"`
			*models.AuditEvent `bson:",inline"`
		}{ev.ID, ev})
	}
	if err := replaceMap(ctx, db, mcoAuditEvents, auditDocs); err != nil {
		return err
	}

	// API tokens
	if err := replaceMap(ctx, db, mcoAPITokens, mapToDocs(d.APITokens, func(t *models.APIToken) any {
		return struct {
			ID string `bson:"_id"`
			*models.APIToken `bson:",inline"`
		}{t.ID, t}
	})); err != nil {
		return err
	}

	// Notifications singleton
	if d.Notifications != nil {
		if _, err := db.Collection(mcoNotifications).ReplaceOne(
			ctx,
			bson.D{{Key: "_id", Value: "singleton"}},
			struct {
				ID string `bson:"_id"`
				*models.NotificationConfig `bson:",inline"`
			}{"singleton", d.Notifications},
			options.Replace().SetUpsert(true),
		); err != nil {
			return err
		}
	}

	// Snapshot policies
	if err := replaceMap(ctx, db, mcoSnapshotPolicies, mapToDocs(d.SnapshotPolicies, func(p *models.SnapshotPolicy) any {
		return struct {
			ID string `bson:"_id"`
			*models.SnapshotPolicy `bson:",inline"`
		}{p.ID, p}
	})); err != nil {
		return err
	}

	return nil
}

// replaceMap empties a collection then bulk-inserts the provided
// documents. Executed as a single DeleteMany + InsertMany pair; not
// transactional (Mongo standalone doesn't support multi-doc txn
// without a replica set) — acceptable because Save is only called
// from the 30s persist ticker and a mid-save crash just means the
// next tick reinstates the correct state.
func replaceMap(ctx context.Context, db *mongo.Database, collection string, docs []any) error {
	coll := db.Collection(collection)
	if _, err := coll.DeleteMany(ctx, bson.D{}); err != nil {
		return fmt.Errorf("drop %s: %w", collection, err)
	}
	if len(docs) == 0 {
		return nil
	}
	if _, err := coll.InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("insert %s: %w", collection, err)
	}
	return nil
}

// loadMapByID streams an entire collection and builds a keyed map by
// calling keyOf on each decoded value. Generic so the same plumbing
// covers hosts / users / groups / roles / idps.
func loadMapByID[T any](ctx context.Context, db *mongo.Database, col string, dst map[string]*T, keyOf func(*T) string) error {
	cur, err := db.Collection(col).Find(ctx, bson.D{})
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		v := new(T)
		if err := cur.Decode(v); err != nil {
			return err
		}
		k := keyOf(v)
		if k == "" {
			continue
		}
		dst[k] = v
	}
	return cur.Err()
}

// mapToDocs flattens a keyed map into a slice of BSON docs. The
// toDoc closure controls exactly how each entity is serialised,
// including the _id field, so we get consistent keys per collection.
func mapToDocs[T any](m map[string]*T, toDoc func(*T) any) []any {
	if len(m) == 0 {
		return nil
	}
	out := make([]any, 0, len(m))
	for _, v := range m {
		out = append(out, toDoc(v))
	}
	return out
}
