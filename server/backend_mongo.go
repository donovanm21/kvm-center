package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
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
// Saves replace retained records before pruning obsolete ones, and use dirty
// hints for cold collections. Writes remain nontransactional across records and
// collections; a failed Save requires a full-snapshot retry.
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
	mcoHosts            = "hosts"
	mcoVMs              = "vms"         // flat per-VM documents keyed by uuid
	mcoStorage          = "storage"     // flat per-pool docs keyed by host_id+name
	mcoVMNotes          = "vm_notes"    // keyed by vm_uuid
	mcoGuestData        = "guest_data"  // keyed by vm_uuid, updated_at TTL
	mcoMigrations       = "migrations"  // append-only, indexed by started_at desc
	mcoCommandLog       = "command_log" // per-/api/command invocation, indexed by submitted_at + TTL on expires_at
	mcoUsers            = "users"       // username unique
	mcoGroups           = "groups"
	mcoRoles            = "roles"
	mcoSessions         = "sessions" // TTL on expires_at
	mcoIDPs             = "idps"
	mcoFeatures         = "features"    // single doc with _id:"singleton"
	mcoServerMeta       = "server_meta" // _id:"singleton" — console_secret etc.
	mcoAuditEvents      = "audit_events"
	mcoSnapshotPolicies = "snapshot_policies"
	mcoNotifications    = "notifications"       // singleton _id:"singleton"
	mcoMetrics          = "metrics"             // singleton _id:"singleton"
	mcoAuditShipping    = "audit_shipping"      // singleton _id:"singleton" (v5.51.1)
	mcoBackupSchedule   = "backup_schedule"     // singleton _id:"singleton" (v5.51.1)
	mcoDiscordGateway   = "discord_gateway"     // singleton _id:"singleton" (v5.52.0)
	mcoRemovedHosts     = "removed_hosts"       // keyed by host_id (v5.54.0)
	mcoPendingEnrol     = "pending_enrollments" // keyed by enrolment ID (v5.55.0)
	mcoAlarms           = "alarms"
	mcoAPITokens        = "api_tokens"       // keyed by token ID
	mcoHypervisorCerts  = "hypervisor_certs" // keyed by host ID
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
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = cli.Disconnect(closeCtx)
		closeCancel()
		return nil, fmt.Errorf("mongo ping: %w", err)
	}
	db := cli.Database(b.dbName)
	// Refuse known legacy representations before creating any indexes. TTL
	// activation is a write-side effect even when Load later returns an error.
	if err := preflightCollections(ctx, db); err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = cli.Disconnect(closeCtx)
		closeCancel()
		return nil, err
	}
	b.client = cli
	b.db = db
	if err := b.ensureIndexes(ctx); err != nil {
		return nil, fmt.Errorf("mongo indexes: %w", err)
	}
	return b.db, nil
}

// invalidateOnTransientError nulls the cached client/db when an error
// looks like a network/timeout/topology failure so the next dial()
// re-connects instead of returning the stale cached handle. Mongo
// flapping after boot used to require a server restart to recover —
// the cached client kept returning connection-refused on every call
// without ever attempting to re-dial. With this hook, the first
// transient error tears down the cache; the next op reconnects.
//
// We deliberately keep the heuristic narrow: only error strings that
// strongly imply transport-level failures trigger the invalidation.
// Auth or schema errors stay on the cached client so the caller
// surfaces them immediately rather than silently recycling.
func (b *MongoBackend) invalidateOnTransientError(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	transient := false
	for _, sig := range []string{
		"connection refused",
		"connection reset",
		"server selection error",
		"i/o timeout",
		"context deadline exceeded",
		"no reachable servers",
		"socket was unexpectedly closed",
		"EOF",
	} {
		if strings.Contains(msg, sig) {
			transient = true
			break
		}
	}
	if !transient {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client != nil {
		// Disconnect on a fresh context — the caller's ctx may already
		// be done, and Disconnect has its own internal timeout anyway.
		go func(c *mongo.Client) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = c.Disconnect(ctx)
		}(b.client)
	}
	b.client, b.db = nil, nil
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
		// TTL on expires_at — set only for terminal records
		// (complete/failed/cancelled). Active records omit the field,
		// so they never expire. Tier S #2, v5.32.17.
		{mcoMigrations, bson.D{{Key: "expires_at", Value: 1}}, options.Index().SetExpireAfterSeconds(0)},
		// Command log — indexed for reconciler queries, TTL sweeps
		// terminal entries 30d after completion. Tier S #4, v5.32.17.
		{mcoCommandLog, bson.D{{Key: "submitted_at", Value: -1}}, nil},
		{mcoCommandLog, bson.D{{Key: "outcome", Value: 1}}, nil},
		{mcoCommandLog, bson.D{{Key: "expires_at", Value: 1}}, options.Index().SetExpireAfterSeconds(0)},
		{mcoAuditEvents, bson.D{{Key: "at", Value: -1}}, nil},
		{mcoAuditEvents, bson.D{{Key: "actor_id", Value: 1}}, nil},
		{mcoAuditEvents, bson.D{{Key: "action", Value: 1}}, nil},
		// v5.60.18: TTL on `at` — audit events older than 365 days
		// roll off the Mongo collection. The in-memory ring already
		// caps at 10k entries (auditRetentionMax in audit.go), but
		// the on-disk collection grew unboundedly pre-v5.60.18.
		// 365d preserves a year of history for compliance review
		// without letting a high-event-rate fleet fill the disk.
		// Operators wanting longer retention should ship audit
		// events to an external store via Settings → Audit shipping.
		{mcoAuditEvents, bson.D{{Key: "at", Value: 1}}, options.Index().SetExpireAfterSeconds(365 * 24 * 60 * 60)},
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
func (b *MongoBackend) Load(ctx context.Context) (_ *persistData, err error) {
	defer func() { b.invalidateOnTransientError(err) }()
	db, err := b.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := rejectLegacyCollections(ctx, db); err != nil {
		return nil, err
	}
	legacy, err := loadLegacyHistory(ctx, db)
	if err != nil {
		return nil, err
	}
	history, err := loadHostHistory(ctx, db)
	if err != nil {
		return nil, err
	}
	tags, err := loadVMTagState(ctx, db)
	if err != nil {
		return nil, err
	}
	updates, err := loadGuestUpdateState(ctx, db)
	if err != nil {
		return nil, err
	}
	affinity, err := loadAffinityState(ctx, db)
	if err != nil {
		return nil, err
	}
	pd := &persistData{
		GuestUpdates:  updates,
		Affinity:      affinity,
		VMTagState:    tags,
		HostHistory:   history,
		LegacyHistory: legacy,
		Hosts:         map[string]*models.Host{},
		VMs:           map[string][]*models.VM{},
		Storage:       map[string][]*models.StoragePool{},
		VMNotes:       map[string]string{},
		GuestData:     map[string]*guestEntry{},
		Users:         map[string]*models.User{},
		Groups:        map[string]*models.Group{},
		Roles:         map[string]*models.Role{},
		Sessions:      map[string]*models.Session{},
		IDPs:          map[string]*models.IdentityProvider{},
		RemovedHosts:  map[string]*models.RemovedHost{},
	}

	// Hosts
	if err := loadMapByID[models.Host](ctx, db, mcoHosts, pd.Hosts, func(h *models.Host) string { return h.ID }); err != nil {
		return nil, err
	}

	// VMs — flat collection grouped back into map[host_id][]*VM
	// Pre-v5.50 these cursor blocks reused a single `cur` variable
	// without closing previous iterations. cur.All() does close the
	// cursor on success/error, but the for-range Next() blocks below
	// (VMNotes, GuestData) do not — and any reuse without defer leaks
	// the prior cursor on the server side until idle-cursor timeout
	// (10 min). Each Find now scopes its own cursor inside a closure
	// with an explicit defer Close so the server-side resource is
	// released regardless of decode failure or context cancellation.
	if err := func() error {
		cur, err := db.Collection(mcoVMs).Find(ctx, bson.D{})
		if err != nil {
			return err
		}
		defer cur.Close(ctx)
		var vms []*models.VM
		if err := cur.All(ctx, &vms); err != nil {
			return err
		}
		for _, v := range vms {
			pd.VMs[v.HostID] = append(pd.VMs[v.HostID], v)
		}
		return nil
	}(); err != nil {
		return nil, err
	}

	// Strictly load passive inventory; malformed or duplicate records must not
	// disappear when the typed snapshot is later saved.
	for _, col := range []string{mcoStorage, mcoVMNotes, mcoRemovedHosts, mcoGuestData} {
		cur, err := db.Collection(col).Find(ctx, bson.D{})
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for cur.Next(ctx) {
			raw := cur.Current
			id, err := entityIdentity(raw, col)
			if err != nil {
				cur.Close(ctx)
				return nil, err
			}
			seenID := id
			if col == mcoRemovedHosts {
				seenID = strings.ToLower(id)
			}
			if seen[seenID] {
				cur.Close(ctx)
				return nil, errors.New("duplicate passive entity identity")
			}
			seen[seenID] = true
			typ, aliases, _ := entitySchema(col)
			// Use the typed projection as candidate, validating all original
			// values without silently interpreting an opaque alias.
			model := reflect.New(typ).Interface()
			if err := bson.Unmarshal(raw, model); err != nil {
				cur.Close(ctx)
				return nil, err
			}
			projection, _ := bson.Marshal(model)
			es, _ := bson.Raw(projection).Elements()
			doc := bson.D{{Key: "_id", Value: id}}
			for _, el := range es {
				doc = append(doc, bson.E{Key: el.Key(), Value: el.Value()})
			}
			candidate, _ := bson.Marshal(doc)
			if _, err := preserveEntityBSON(raw, candidate, typ, aliases); err != nil {
				cur.Close(ctx)
				return nil, err
			}
			if col == mcoStorage {
				pool := model.(*models.StoragePool)
				pd.Storage[pool.HostID] = append(pd.Storage[pool.HostID], pool)
			} else if col == mcoGuestData {
				report := model.(*entityGuestReport)
				pd.GuestData[report.UUID] = &report.Entry
			} else if col == mcoRemovedHosts {
				record := model.(*models.RemovedHost)
				pd.RemovedHosts[strings.ToLower(record.HostID)] = record
			} else {
				note := model.(*entityVMNote)
				pd.VMNotes[note.UUID] = note.Text
			}
		}
		err = cur.Err()
		cur.Close(ctx)
		if err != nil {
			return nil, err
		}
	}
	if err := validateEntitySnapshot(pd); err != nil {
		return nil, err
	}

	// Migrations — load all, preserve order (started_at desc on read)
	if err := func() error {
		opts := options.Find().SetSort(bson.D{{Key: "started_at", Value: -1}})
		cur, err := db.Collection(mcoMigrations).Find(ctx, bson.D{}, opts)
		if err != nil {
			return err
		}
		defer cur.Close(ctx)
		return cur.All(ctx, &pd.Migrations)
	}(); err != nil {
		return nil, err
	}

	if pd.Alarms, err = loadAlarmRecords(ctx, db); err != nil {
		return nil, err
	}
	if err := validateAlarmInventory(pd); err != nil {
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

	if pd.Sessions, err = loadSessionRecords(ctx, db); err != nil {
		return nil, err
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

	// Audit admission is bounded and strict before typed records can be rewritten.
	pd.Audit, err = loadAuditRecords(ctx, db)
	if err != nil {
		return nil, err
	}

	// Snapshot policies (v5.3) — keyed map, same pattern as users.
	pd.SnapshotPolicies = map[string]*models.SnapshotPolicy{}
	if err := loadMapByID[models.SnapshotPolicy](ctx, db, mcoSnapshotPolicies, pd.SnapshotPolicies, func(p *models.SnapshotPolicy) string { return p.ID }); err != nil {
		return nil, err
	}

	// API tokens (v5.5) keyed map.
	pd.APITokens, err = loadTokenRecords(ctx, db)
	if err != nil {
		return nil, err
	}

	// Hypervisor certs (v5.10.0).
	pd.HypervisorCerts = map[string]*models.HypervisorCert{}
	if err := loadMapByID[models.HypervisorCert](ctx, db, mcoHypervisorCerts, pd.HypervisorCerts, func(c *models.HypervisorCert) string { return c.HostID }); err != nil {
		return nil, err
	}

	// Command log (v5.32.17). Mongo TTL handles its own pruning.
	pd.CommandLog = map[string]*models.CommandLogEntry{}
	if err := loadMapByID[models.CommandLogEntry](ctx, db, mcoCommandLog, pd.CommandLog, func(c *models.CommandLogEntry) string { return c.RequestID }); err != nil {
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

	// Metrics (v5.13) singleton.
	var metrics models.MetricsConfig
	err = db.Collection(mcoMetrics).FindOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}).Decode(&metrics)
	if err == nil {
		pd.Metrics = &metrics
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}

	// AuditShipping (v5.51.1) singleton. Pre-v5.51.1 the mongo backend
	// silently dropped this on Save, so any operator-set syslog/HTTP
	// shipping config evaporated on the next redeploy.
	var ship models.AuditShipping
	err = db.Collection(mcoAuditShipping).FindOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}).Decode(&ship)
	if err == nil {
		pd.AuditShipping = &ship
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}

	// BackupSchedule (v5.51.1) singleton. Same drop-on-redeploy bug as
	// audit_shipping above — fixed together.
	var bsched models.BackupSchedule
	err = db.Collection(mcoBackupSchedule).FindOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}).Decode(&bsched)
	if err == nil {
		pd.BackupSchedule = &bsched
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}

	// DiscordGateway (v5.52.0) singleton. Carries pairing creds +
	// state-machine value; survives restart so the operator doesn't
	// have to re-pair.
	var dgw models.DiscordGateway
	err = db.Collection(mcoDiscordGateway).FindOne(ctx, bson.D{{Key: "_id", Value: "singleton"}}).Decode(&dgw)
	if err == nil {
		pd.DiscordGateway = &dgw
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}

	// PendingEnrollments (v5.55.0) — keyed by enrolment ID. Survives
	// restart so an in-flight approval workflow can resume across
	// kvm-center bounces without making the operator re-submit.
	pd.PendingEnrollments = map[string]*models.PendingEnrollment{}
	if err := loadMapByID[models.PendingEnrollment](ctx, db, mcoPendingEnrol, pd.PendingEnrollments, func(p *models.PendingEnrollment) string { return p.ID }); err != nil {
		return nil, err
	}

	return pd, nil
}

// Save replaces every collection's contents with the provided
// persistData snapshot. Ordered replacement and dirty hints do not provide
// a crash-atomic checkpoint across records or collections.
func (b *MongoBackend) Save(ctx context.Context, d *persistData) (err error) {
	defer func() { b.invalidateOnTransientError(err) }()
	if d != nil && d.EntityOverlay != nil {
		return errors.New("entity overlay requires explicit fresh-destination restore")
	}
	if d == nil {
		return errors.New("missing persistence snapshot")
	}
	if err := validateEntitySnapshot(d); err != nil {
		return err
	}
	if err := d.HostHistory.validate(); err != nil {
		return err
	}
	if err := d.VMTagState.validate(); err != nil {
		return err
	}
	db, err := b.dial(ctx)
	if err != nil {
		return err
	}

	if err := rejectLegacyCollections(ctx, db); err != nil {
		return err
	}

	// Reject unsupported feature namespaces before any replacement writes. This is
	// a compatibility preflight, not a transaction or protection from outside writers.
	if _, err := loadHostHistory(ctx, db); err != nil {
		return err
	}
	if _, err := loadVMTagState(ctx, db); err != nil {
		return err
	}

	if err := preflightLegacyHistorySave(ctx, db, d, d.restoreLegacyHistory); err != nil {
		return err
	}
	if err := preflightGuestUpdates(ctx, db, d); err != nil {
		return err
	}
	if err := preflightAffinitySave(ctx, db, d); err != nil {
		return err
	}
	if err := saveGuestUpdates(ctx, db, d); err != nil {
		return err
	}
	// Persist the reservation before other mutable records. A later failure is
	// uncertain and remains blocking; it is never rolled back into permission.
	if err := saveAffinityState(ctx, db, d); err != nil {
		return err
	}
	// Hosts. Every embed below uses bson:",inline" so the entity's
	// fields land at the document root rather than nested under the
	// Go struct's field name — otherwise the default decoder can't
	// hydrate the Host back from a "host" sub-object.
	hostDocs := mapToDocs(d.Hosts, func(h *models.Host) any {
		return struct {
			ID           string `bson:"_id"`
			*models.Host `bson:",inline"`
		}{h.ID, h}
	})

	// VMs — flatten nested map. Fall back to host|name for the _id when
	// the UUID field is empty (pre-v4 data) so docs don't collide.
	//
	// Refuse duplicate identities before writes. Picking an arbitrary host
	// entry could discard conflicting state or attach opaque fields to the
	// wrong VM during migration.
	var vmDocs []any
	seenVM := map[string]bool{}
	for hostID, vms := range d.VMs {
		for _, v := range vms {
			id := strings.ToLower(v.UUID)
			if id == "" {
				id = hostID + "|" + v.Name
			}
			if seenVM[id] {
				return errors.New("duplicate VM identity in persistence snapshot")
			}
			seenVM[id] = true
			vmDocs = append(vmDocs, struct {
				ID string     `bson:"_id"`
				VM *models.VM `bson:",inline"`
			}{id, v})
		}
	}
	// Validate all supported entity plans before any replacement or deletion,
	// including username-owner retirement.
	hostDocs, err = prepareEntityDocumentsFrom(ctx, db, mcoHosts, hostDocs, restoreEntityRecords(d, mcoHosts))
	if err != nil {
		return fmt.Errorf("host preservation preflight: %w", err)
	}
	vmDocs, err = prepareEntityDocumentsFrom(ctx, db, mcoVMs, vmDocs, restoreEntityRecords(d, mcoVMs))
	if err != nil {
		return fmt.Errorf("VM preservation preflight: %w", err)
	}
	usersDocs := mapToDocs(d.Users, func(v *models.User) any {
		return struct {
			ID           string `bson:"_id"`
			*models.User `bson:",inline"`
		}{v.ID, v}
	})
	usersDocs, err = prepareEntityDocumentsFrom(ctx, db, mcoUsers, usersDocs, restoreEntityRecords(d, mcoUsers))
	if err != nil {
		return fmt.Errorf("users preservation preflight: %w", err)
	}
	groupsDocs := mapToDocs(d.Groups, func(v *models.Group) any {
		return struct {
			ID            string `bson:"_id"`
			*models.Group `bson:",inline"`
		}{v.ID, v}
	})
	groupsDocs, err = prepareEntityDocumentsFrom(ctx, db, mcoGroups, groupsDocs, restoreEntityRecords(d, mcoGroups))
	if err != nil {
		return fmt.Errorf("groups preservation preflight: %w", err)
	}
	rolesDocs := mapToDocs(d.Roles, func(v *models.Role) any {
		return struct {
			ID           string `bson:"_id"`
			*models.Role `bson:",inline"`
		}{v.ID, v}
	})
	rolesDocs, err = prepareEntityDocumentsFrom(ctx, db, mcoRoles, rolesDocs, restoreEntityRecords(d, mcoRoles))
	if err != nil {
		return fmt.Errorf("roles preservation preflight: %w", err)
	}
	passivePlans := map[string][]any{}
	candidates := overlayCandidates(d)
	for _, col := range []string{mcoStorage, mcoVMNotes, mcoRemovedHosts, mcoGuestData, mcoAuditEvents, mcoAPITokens, mcoSessions, mcoAlarms} {
		prepared, err := prepareEntityDocumentsFrom(ctx, db, col, candidates[col], restoreEntityRecords(d, col))
		if err != nil {
			return fmt.Errorf("%s preservation preflight: %w", col, err)
		}
		passivePlans[col] = prepared
	}
	if d.restoreLegacyHistory {
		if err := restoreLegacyHistoryRecords(ctx, db, d.LegacyHistory); err != nil {
			return err
		}
	}
	if err := replaceMap(ctx, db, mcoHosts, hostDocs); err != nil {
		return err
	}
	if err := replaceMap(ctx, db, mcoVMs, vmDocs); err != nil {
		return err
	}

	if err := replaceMap(ctx, db, mcoStorage, passivePlans[mcoStorage]); err != nil {
		return err
	}
	if err := replaceMap(ctx, db, mcoVMNotes, passivePlans[mcoVMNotes]); err != nil {
		return err
	}

	if err := replaceMap(ctx, db, mcoGuestData, passivePlans[mcoGuestData]); err != nil {
		return err
	}

	// Migrations — append-only list, persisted as the full array.
	// Terminal records carry expires_at = CompletedAt + 30d so the
	// TTL index sweeps them; active records leave the field unset
	// so they stick around indefinitely. Tier S #2, v5.32.17.
	var migDocs []any
	for _, m := range d.Migrations {
		doc := struct {
			ID              string                  `bson:"_id"`
			MigrationRecord *models.MigrationRecord `bson:",inline"`
			ExpiresAt       *time.Time              `bson:"expires_at,omitempty"`
		}{ID: m.ID, MigrationRecord: m}
		if isTerminalMigration(m.Status) && m.CompletedAt > 0 {
			t := time.Unix(m.CompletedAt, 0).Add(migrationRetentionDays * 24 * time.Hour)
			doc.ExpiresAt = &t
		}
		migDocs = append(migDocs, doc)
	}
	if err := replaceMap(ctx, db, mcoMigrations, migDocs); err != nil {
		return err
	}

	if err := replaceMapIfDirty(ctx, db, mcoUsers, "users", d, usersDocs); err != nil {
		return err
	}
	if err := replaceMapIfDirty(ctx, db, mcoGroups, "groups", d, groupsDocs); err != nil {
		return err
	}
	if err := replaceMapIfDirty(ctx, db, mcoRoles, "roles", d, rolesDocs); err != nil {
		return err
	}
	if err := replaceMapIfDirty(ctx, db, mcoIDPs, "idps", d, mapToDocs(d.IDPs, func(i *models.IdentityProvider) any {
		return struct {
			ID                       string `bson:"_id"`
			*models.IdentityProvider `bson:",inline"`
		}{i.ID, i}
	})); err != nil {
		return err
	}

	if err := replaceMap(ctx, db, mcoAlarms, passivePlans[mcoAlarms]); err != nil {
		return err
	}
	if err := replaceMap(ctx, db, mcoSessions, passivePlans[mcoSessions]); err != nil {
		return err
	}

	// Features singleton upsert
	if d.Features != nil && isDirty(d, "features") {
		if _, err := db.Collection(mcoFeatures).ReplaceOne(
			ctx,
			bson.D{{Key: "_id", Value: "singleton"}},
			struct {
				ID               string `bson:"_id"`
				*models.Features `bson:",inline"`
			}{"singleton", d.Features},
			options.Replace().SetUpsert(true),
		); err != nil {
			return err
		}
	}

	// Server metadata singleton
	if isDirty(d, "server_meta") {
		if _, err := db.Collection(mcoServerMeta).ReplaceOne(
			ctx,
			bson.D{{Key: "_id", Value: "singleton"}},
			bson.M{"_id": "singleton", "console_secret": d.ConsoleSecret},
			options.Replace().SetUpsert(true),
		); err != nil {
			return err
		}
	}

	// Audit events use the strict, preservation-aware plan prepared before writes.
	if err := replaceMap(ctx, db, mcoAuditEvents, passivePlans[mcoAuditEvents]); err != nil {
		return err
	}

	// Preserve tokens even when a dirty hint omits their known fields.
	if err := replaceMap(ctx, db, mcoAPITokens, passivePlans[mcoAPITokens]); err != nil {
		return err
	}

	// Metrics singleton
	if d.Metrics != nil && isDirty(d, "metrics") {
		if _, err := db.Collection(mcoMetrics).ReplaceOne(
			ctx,
			bson.D{{Key: "_id", Value: "singleton"}},
			struct {
				ID                    string `bson:"_id"`
				*models.MetricsConfig `bson:",inline"`
			}{"singleton", d.Metrics},
			options.Replace().SetUpsert(true),
		); err != nil {
			return err
		}
	}

	// Notifications singleton
	if d.Notifications != nil && isDirty(d, "notifications") {
		if _, err := db.Collection(mcoNotifications).ReplaceOne(
			ctx,
			bson.D{{Key: "_id", Value: "singleton"}},
			struct {
				ID                         string `bson:"_id"`
				*models.NotificationConfig `bson:",inline"`
			}{"singleton", d.Notifications},
			options.Replace().SetUpsert(true),
		); err != nil {
			return err
		}
	}

	// AuditShipping singleton (v5.51.1). Mirrors notifications/metrics —
	// upsert the singleton doc when the dirty hint flags it. Pre-v5.51.1
	// this branch was missing entirely, so operator syslog/HTTP config
	// only lived in memory and evaporated on every redeploy.
	if d.AuditShipping != nil && isDirty(d, "audit_shipping") {
		if _, err := db.Collection(mcoAuditShipping).ReplaceOne(
			ctx,
			bson.D{{Key: "_id", Value: "singleton"}},
			struct {
				ID                    string `bson:"_id"`
				*models.AuditShipping `bson:",inline"`
			}{"singleton", d.AuditShipping},
			options.Replace().SetUpsert(true),
		); err != nil {
			return err
		}
	}

	// BackupSchedule singleton (v5.51.1).
	if d.BackupSchedule != nil && isDirty(d, "backup_schedule") {
		if _, err := db.Collection(mcoBackupSchedule).ReplaceOne(
			ctx,
			bson.D{{Key: "_id", Value: "singleton"}},
			struct {
				ID                     string `bson:"_id"`
				*models.BackupSchedule `bson:",inline"`
			}{"singleton", d.BackupSchedule},
			options.Replace().SetUpsert(true),
		); err != nil {
			return err
		}
	}

	if err := replaceMapIfDirty(ctx, db, mcoRemovedHosts, "removed_hosts", d, passivePlans[mcoRemovedHosts]); err != nil {
		return err
	}

	// PendingEnrollments (v5.55.0) — keyed by enrolment ID.
	if isDirty(d, "pending_enrollments") {
		if err := replaceMap(ctx, db, mcoPendingEnrol, mapToDocs(d.PendingEnrollments, func(p *models.PendingEnrollment) any {
			return struct {
				ID                        string `bson:"_id"`
				*models.PendingEnrollment `bson:",inline"`
			}{p.ID, p}
		})); err != nil {
			return err
		}
	}

	// DiscordGateway singleton (v5.52.0).
	if d.DiscordGateway != nil && isDirty(d, "discord_gateway") {
		if _, err := db.Collection(mcoDiscordGateway).ReplaceOne(
			ctx,
			bson.D{{Key: "_id", Value: "singleton"}},
			struct {
				ID                     string `bson:"_id"`
				*models.DiscordGateway `bson:",inline"`
			}{"singleton", d.DiscordGateway},
			options.Replace().SetUpsert(true),
		); err != nil {
			return err
		}
	}

	// Hypervisor certs
	if err := replaceMapIfDirty(ctx, db, mcoHypervisorCerts, "hypervisor_certs", d, mapToDocs(d.HypervisorCerts, func(c *models.HypervisorCert) any {
		return struct {
			ID                     string `bson:"_id"`
			*models.HypervisorCert `bson:",inline"`
		}{c.HostID, c}
	})); err != nil {
		return err
	}

	// Snapshot policies
	if err := replaceMapIfDirty(ctx, db, mcoSnapshotPolicies, "snapshot_policies", d, mapToDocs(d.SnapshotPolicies, func(p *models.SnapshotPolicy) any {
		return struct {
			ID                     string `bson:"_id"`
			*models.SnapshotPolicy `bson:",inline"`
		}{p.ID, p}
	})); err != nil {
		return err
	}

	// Command log (v5.32.17). Inline the whole entry — _id == RequestID.
	if err := replaceMapIfDirty(ctx, db, mcoCommandLog, "command_log", d, mapToDocs(d.CommandLog, func(c *models.CommandLogEntry) any {
		return c
	})); err != nil {
		return err
	}

	if err := saveVMTagState(ctx, db, d); err != nil {
		return err
	}
	return saveHostHistory(ctx, db, d)
}

// isDirty returns true when the persistData carries a dirty flag for
// the named collection. First-boot migration paths pass Dirty=nil, in
// which case we conservatively treat everything as dirty to keep the
// initial Mongo load complete.
func isDirty(d *persistData, name string) bool {
	if d == nil || d.Dirty == nil {
		return true
	}
	return d.Dirty[name]
}

// replaceMapIfDirty skips replacement writes when the named
// collection isn't marked dirty. First save after startup has no
// dirty set (nil) — falls through to a full write so the collection
// is in sync before we start trusting the hint.
func replaceMapIfDirty(ctx context.Context, db *mongo.Database, collection, dirtyKey string, d *persistData, docs []any) error {
	if !isDirty(d, dirtyKey) {
		return nil
	}
	return replaceMap(ctx, db, collection, docs)
}

// replaceMap preserves retained IDs while replacing records. Obsolete username
// owners are retired first when needed; other obsolete records are pruned last.
// An interrupted ordered write may leave mixed old/new values, but does not
// remove retained records before their replacements exist. This is not an
// atomic snapshot across records or collections; SaveChecked retains full-retry
// behavior after any partial failure.
func replaceMap(ctx context.Context, db *mongo.Database, collection string, docs []any) error {
	coll := db.Collection(collection)
	if len(docs) == 0 {
		// Genuinely empty now — clearing is the correct end state, and there's
		// no gap to protect since there's nothing to write afterwards.
		if _, err := coll.DeleteMany(ctx, bson.D{}); err != nil {
			return fmt.Errorf("clear %s: %w", collection, err)
		}
		return nil
	}

	models, err := buildReplaceModels(collection, docs)
	if err != nil {
		return err
	}
	// A removed user can own a username needed by a newly created identity.
	// Retire only those obsolete owners before upserting; deleting retained
	// IDs would reintroduce the data-loss interval this ordering avoids.
	// This can commit an intended deletion before a later write fails, so the
	// operation remains nontransactional and requires a full-snapshot retry.
	if collection == mcoUsers {
		if filter := obsoleteUsernameOwners(models); filter != nil {
			if _, err := coll.DeleteMany(ctx, filter); err != nil {
				return fmt.Errorf("retire obsolete username owners: %w", err)
			}
		}
	}
	if _, err := coll.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(true)); err != nil {
		return fmt.Errorf("replace %s: %w", collection, err)
	}
	return nil
}

// buildReplaceModels turns the desired document set into an ordered write
// plan: one upsert per document, then a single delete of everything else.
//
// Split out so the invariant can be tested without a live Mongo — namely that
// the trailing delete's $nin lists every document being kept, so it can only
// ever remove documents that are genuinely gone, and that it sorts last so no
// moment exists where a kept document is absent.
func buildReplaceModels(collection string, docs []any) ([]mongo.WriteModel, error) {
	models := make([]mongo.WriteModel, 0, len(docs)+1)
	ids := make(bson.A, 0, len(docs))
	for _, d := range docs {
		// Marshal once and reuse the raw bytes as the replacement, so the
		// stored document is byte-identical to what InsertMany would have
		// written — no type drift from a bson.M round-trip.
		raw, err := bson.Marshal(d)
		if err != nil {
			return nil, fmt.Errorf("marshal %s doc: %w", collection, err)
		}
		idVal, err := bson.Raw(raw).LookupErr("_id")
		if err != nil {
			return nil, fmt.Errorf("%s doc has no _id: %w", collection, err)
		}
		// Every collection here keys on a string _id (host ID, VM UUID,
		// request ID, "singleton", …). Refuse anything else rather than
		// silently building a $nin that matches nothing and wipes the
		// collection.
		id, ok := idVal.StringValueOK()
		if !ok {
			return nil, fmt.Errorf("%s doc has a non-string _id (%s)", collection, idVal.Type)
		}
		ids = append(ids, id)
		models = append(models, mongo.NewReplaceOneModel().
			SetFilter(bson.D{{Key: "_id", Value: id}}).
			SetReplacement(bson.Raw(raw)).
			SetUpsert(true))
	}
	// Last, and only after every upsert has landed: drop whatever is no
	// longer part of the state.
	models = append(models, mongo.NewDeleteManyModel().
		SetFilter(bson.D{{Key: "_id", Value: bson.D{{Key: "$nin", Value: ids}}}}))
	return models, nil
}

// obsoleteUsernameOwners is built only from a fully validated replacement
// plan. The ID exclusion protects every record retained by that snapshot.
// Existing user IDs cannot be renamed through the user-update API; conflicting
// usernames within one desired snapshot remain errors rather than deleting a
// retained record to satisfy a unique index.
func obsoleteUsernameOwners(plan []mongo.WriteModel) bson.D {
	ids := make(bson.A, 0, len(plan))
	usernames := make(bson.A, 0, len(plan))
	for _, model := range plan {
		replacement, ok := model.(*mongo.ReplaceOneModel)
		if !ok {
			continue
		}
		raw := replacement.Replacement.(bson.Raw)
		ids = append(ids, raw.Lookup("_id").StringValue())
		if username, ok := raw.Lookup("username").StringValueOK(); ok {
			usernames = append(usernames, username)
		}
	}
	if len(usernames) == 0 {
		return nil
	}
	return bson.D{
		{Key: "_id", Value: bson.D{{Key: "$nin", Value: ids}}},
		{Key: "username", Value: bson.D{{Key: "$in", Value: usernames}}},
	}
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
