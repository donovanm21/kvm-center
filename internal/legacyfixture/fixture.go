// Package legacyfixture generates synthetic data from the pinned saved schema.
// It never connects to a database or starts legacy services/jobs.
package legacyfixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"kvm-center/internal/offlinestaging"
)

const Source = "c018b31b74d7d75ed8c49921dc0812bf5999d81f"
const Schema = "synthetic-saved-c018b31-bson-v1"
const HostID = "synthetic-host"
const VMID = "11111111-1111-4111-8111-111111111111"

var clock = time.Date(2024, 1, 1, 0, 0, 0, 123000000, time.UTC)

type Bundle struct {
	Documents map[string][]byte
	Indexes   map[string][]byte
	Checksums map[string]string
}

func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Populate each reachable declared field, including nested entities. Semantic
// overrides below establish cross-references and boundary values. Generic text
// is explicitly synthetic and is never an instruction to activate an endpoint.
func fill(v reflect.Value, name string) {
	if v.Type() == reflect.TypeOf(time.Time{}) {
		v.Set(reflect.ValueOf(clock))
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), name)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				fill(v.Field(i), v.Type().Field(i).Name)
			}
		}
	case reflect.String:
		value := "synthetic-" + strings.ToLower(name)
		if strings.HasSuffix(name, "At") || name == "PendingSince" {
			value = clock.Format(time.RFC3339Nano)
		}
		switch name {
		case "HostID", "Host", "Hosts", "SourceHost", "DestHost":
			value = HostID
		case "UUID", "VMUUID", "VMUUIDs", "VMs":
			value = VMID
		case "GroupIDs":
			value = "synthetic-group"
		case "RoleIDs", "DefaultRoleID":
			value = "synthetic-role"
		case "ProviderID":
			value = "synthetic-idp"
		case "UserID", "OwnerID", "ActorID":
			value = "synthetic-user"
		case "IP":
			value = "192.0.2.10"
		case "URL", "Endpoint":
			value = "https://example.invalid/fixture"
		case "State":
			value = "stopped"
		case "Status", "Outcome":
			value = "cancelled"
		}
		v.SetString(value)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := int64(7)
		if strings.HasSuffix(name, "At") || name == "T" {
			n = clock.Unix()
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(12.125)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0), name)
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		k := reflect.New(v.Type().Key()).Elem()
		fill(k, "key")
		x := reflect.New(v.Type().Elem()).Elem()
		fill(x, name)
		v.SetMapIndex(k, x)
	default:
		panic("unsupported fixture kind: " + v.Type().String())
	}
}
func entity[T any]() *T { p := new(T); fill(reflect.ValueOf(p).Elem(), ""); return p }
func marshal(v any) []byte {
	b, e := bson.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
func document(id string, v any) bson.D {
	var d bson.D
	if err := bson.Unmarshal(marshal(v), &d); err != nil {
		panic(err)
	}
	for _, e := range d {
		if e.Key == "_id" {
			if e.Value != id {
				panic("fixture identity mismatch")
			}
			return d
		}
	}
	return append(bson.D{{Key: "_id", Value: id}}, d...)
}

// Generate includes exact legacy wrappers plus an intentionally unsupported
// field and collection. These must survive preservation and block compatibility.
func Generate() Bundle {
	docs := map[string]bson.D{}
	host := entity[Host]()
	host.ID = HostID
	host.Status = "offline"
	docs["hosts"] = document(HostID, host)
	vm := entity[VM]()
	vm.UUID = VMID
	vm.HostID = HostID
	docs["vms"] = document(VMID, vm)
	pool := entity[StoragePool]()
	docs["storage"] = document(HostID+"|"+pool.Name, pool)
	docs["vm_notes"] = bson.D{{Key: "_id", Value: VMID}, {Key: "vm_uuid", Value: VMID}, {Key: "notes", Value: "synthetic notes"}}
	guest := entity[guestEntry]()
	docs["guest_data"] = bson.D{{Key: "_id", Value: VMID}, {Key: "vm_uuid", Value: VMID}, {Key: "updated_at", Value: clock}, {Key: "entry", Value: guest}}
	migration := entity[MigrationRecord]()
	migration.ID = "synthetic-migration"
	migration.VMName = vm.Name
	migration.RequestID = "synthetic-command"
	docs["migrations"] = append(document(migration.ID, migration), bson.E{Key: "expires_at", Value: time.Unix(migration.CompletedAt, 0).Add(30 * 24 * time.Hour)})
	user := entity[User]()
	user.ID = "synthetic-user"
	docs["users"] = document(user.ID, user)
	group := entity[Group]()
	group.ID = "synthetic-group"
	docs["groups"] = document(group.ID, group)
	role := entity[Role]()
	role.ID = "synthetic-role"
	docs["roles"] = document(role.ID, role)
	idp := entity[IdentityProvider]()
	idp.ID = "synthetic-idp"
	docs["idps"] = document(idp.ID, idp)
	session := entity[Session]()
	session.UserID = user.ID
	session.ExpiresAt = time.Unix(1, 123000000).UTC()
	docs["sessions"] = document(session.Token, session)
	docs["features"] = document("singleton", entity[Features]())
	docs["server_meta"] = bson.D{{Key: "_id", Value: "singleton"}, {Key: "console_secret", Value: "synthetic-not-a-credential"}}
	audit := entity[AuditEvent]()
	audit.ID = "synthetic-audit"
	audit.ActorID = user.ID
	docs["audit_events"] = document(audit.ID, audit)
	token := entity[APIToken]()
	token.ID = "synthetic-token"
	docs["api_tokens"] = document(token.ID, token)
	docs["metrics"] = document("singleton", entity[MetricsConfig]())
	alarm := entity[Alarm]()
	alarm.ID = "synthetic-alarm"
	alarm.Metric = "host.cpu_percent"
	alarm.Threshold = 80
	alarm.ForSeconds = 60
	alarm.Severity = "warning"
	docs["alarms"] = document(alarm.ID, alarm)
	affinity := entity[AffinityRule]()
	affinity.ID = "synthetic-affinity"
	affinity.Kind = "separate"
	affinity.Strength = "should"
	docs["affinity_rules"] = document(affinity.ID, affinity)
	docs["vm_tags"] = bson.D{{Key: "_id", Value: VMID}, {Key: "tags", Value: bson.A{"synthetic-tag"}}}
	point := historyPoint{T: 1704067200, CPUAvg: 12.125, CPUMax: 25.5, MemAvg: 20.125, MemMax: 30.5, StorageAvg: 5.125, StorageMax: 6.5, VMs: 1, N: 7}
	docs["perf_history"] = bson.D{{Key: "_id", Value: HostID}, {Key: "host", Value: HostID}, {Key: "tiers", Value: bson.D{{Key: "5m", Value: []historyPoint{point}}, {Key: "1h", Value: []historyPoint{point}}, {Key: "1d", Value: []historyPoint{point}}, {Key: "unknown-preserve", Value: []historyPoint{point}}}}}
	docs["notifications"] = document("singleton", entity[NotificationConfig]())
	docs["audit_shipping"] = document("singleton", entity[AuditShipping]())
	schedule := entity[BackupSchedule]()
	schedule.EveryHours = 0
	schedule.CronExpr = "0 2 * * *"
	schedule.DestPath = "/synthetic-do-not-run"
	docs["backup_schedule"] = document("singleton", schedule)
	retired := entity[RemovedHost]()
	retired.HostID = "synthetic-retired-host"
	docs["removed_hosts"] = document(retired.HostID, retired)
	enrol := entity[PendingEnrollment]()
	enrol.ID = "synthetic-enrollment"
	docs["pending_enrollments"] = document(enrol.ID, enrol)
	docs["discord_gateway"] = document("singleton", entity[DiscordGateway]())
	docs["hypervisor_certs"] = document(HostID, entity[HypervisorCert]())
	policy := entity[SnapshotPolicy]()
	policy.ID = "synthetic-policy"
	docs["snapshot_policies"] = document(policy.ID, policy)
	command := entity[CommandLogEntry]()
	command.RequestID = "synthetic-command"
	command.Action = "MIGRATE"
	command.VMName = vm.Name
	if command.LastProgress != nil {
		command.LastProgress.RequestID = command.RequestID
	}
	docs["command_log"] = document(command.RequestID, command)
	docs["unknown_future"] = bson.D{{Key: "_id", Value: "synthetic-future"}, {Key: "typed", Value: primitive.Binary{Subtype: 0x80, Data: []byte{1, 0, 255}}}}
	out := Bundle{Documents: map[string][]byte{}, Indexes: map[string][]byte{}, Checksums: map[string]string{}}
	for name, d := range docs {
		d = append(d, bson.E{Key: "unsupported_extra", Value: bson.D{{Key: "nested", Value: bson.A{int32(3), int64(4), nil, primitive.DateTime(-1), primitive.NewDecimal128(7, 9)}}}})
		out.Documents[name] = marshal(d)
		out.Indexes[name] = marshal(bson.D{{Key: "v", Value: int32(2)}, {Key: "name", Value: "_id_"}, {Key: "key", Value: bson.D{{Key: "_id", Value: int32(1)}}}})
	}
	addIndex := func(col, key string, dir int32, unique, sparse bool, ttl *int32) {
		d := bson.D{{Key: "v", Value: int32(2)}, {Key: "name", Value: fmt.Sprintf("%s_%d", key, dir)}, {Key: "key", Value: bson.D{{Key: key, Value: dir}}}}
		if unique {
			d = append(d, bson.E{Key: "unique", Value: true})
		}
		if sparse {
			d = append(d, bson.E{Key: "sparse", Value: true})
		}
		if ttl != nil {
			d = append(d, bson.E{Key: "expireAfterSeconds", Value: *ttl})
		}
		out.Indexes[col] = append(out.Indexes[col], marshal(d)...)
	}
	addIndex("vms", "uuid", 1, true, true, nil)
	addIndex("vms", "host_id", 1, false, false, nil)
	addIndex("users", "username", 1, true, true, nil)
	for _, v := range [][3]string{{"migrations", "started_at", "-1"}, {"command_log", "submitted_at", "-1"}, {"command_log", "outcome", "1"}, {"audit_events", "at", "-1"}, {"audit_events", "actor_id", "1"}, {"audit_events", "action", "1"}} {
		dir := int32(1)
		if v[2] == "-1" {
			dir = -1
		}
		addIndex(v[0], v[1], dir, false, false, nil)
	}
	for _, v := range []struct {
		col, key string
		seconds  int32
	}{{"migrations", "expires_at", 0}, {"command_log", "expires_at", 0}, {"audit_events", "at", 31536000}, {"sessions", "expires_at", 0}, {"guest_data", "updated_at", 300}} {
		n := v.seconds
		addIndex(v.col, v.key, 1, false, false, &n)
	}
	for n, b := range out.Documents {
		out.Checksums[n+".bson"] = hash(b)
		out.Checksums[n+".indexes.bson"] = hash(out.Indexes[n])
	}
	return out
}

func Write(dir string, s Bundle) error {
	m := offlinestaging.Input{Contract: offlinestaging.Contract, SourceSchema: Schema, SourceBuild: Source, TargetArtifactSHA256: strings.Repeat("a", 64), VerificationClock: clock.Format(time.RFC3339Nano), ConsistencyEvidence: "single deterministic synthetic generator; no live data", CompanionEvidence: "synthetic only; no deployed artifact or recovery claim"}
	names := []string{}
	for n := range s.Documents {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		m.Collections = append(m.Collections, offlinestaging.Collection{Name: n, Documents: 1, DataSHA256: hash(s.Documents[n]), IndexSHA256: hash(s.Indexes[n])})
		if err := os.WriteFile(filepath.Join(dir, n+".bson"), s.Documents[n], 0600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, n+".indexes.bson"), s.Indexes[n], 0600); err != nil {
			return err
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "snapshot.json"), b, 0600)
}
