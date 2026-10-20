package legacyfixture

import (
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/x/bsonx/bsoncore"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const CompositionSchema = "synthetic-supported-composition-v1"

// GenerateCompositionEntities is authored independently. It never filters a full
// adversarial fixture. Other independently versioned components retain their own
// source contracts (notably saved-source tags versus deployed-source history).
func GenerateCompositionEntities() Bundle {
	opaque := primitive.NewDecimal128(211, 223)
	docs := map[string]bson.D{
		"hosts":         {{Key: "_id", Value: HostID}, {Key: "id", Value: HostID}, {Key: "status", Value: "offline"}},
		"vms":           {{Key: "_id", Value: VMID}, {Key: "uuid", Value: VMID}, {Key: "host_id", Value: HostID}, {Key: "name", Value: "composition-vm"}, {Key: "state", Value: "stopped"}},
		"storage":       {{Key: "_id", Value: HostID + "|CompositionPool"}, {Key: "hostid", Value: HostID}, {Key: "name", Value: "CompositionPool"}, {Key: "capacitygb", Value: float64(100)}},
		"vm_notes":      {{Key: "_id", Value: VMID}, {Key: "vm_uuid", Value: VMID}, {Key: "notes", Value: "composition original note"}},
		"users":         {{Key: "_id", Value: "fixture-reader"}, {Key: "id", Value: "fixture-reader"}, {Key: "username", Value: "fixture-reader"}, {Key: "enabled", Value: true}, {Key: "roleids", Value: []string{"fixture-role"}}},
		"groups":        {{Key: "_id", Value: "fixture-group"}, {Key: "id", Value: "fixture-group"}, {Key: "name", Value: "composition group"}, {Key: "roleids", Value: []string{"fixture-role"}}},
		"roles":         {{Key: "_id", Value: "fixture-role"}, {Key: "id", Value: "fixture-role"}, {Key: "name", Value: "composition role"}, {Key: "permissions", Value: []string{"host.view", "user.manage", "vm.view", "vm.edit", "host.manage"}}},
		"removed_hosts": {{Key: "_id", Value: "composition-retired"}, {Key: "hostid", Value: "composition-retired"}, {Key: "removedat", Value: clock}, {Key: "reason", Value: "composition retained retirement"}},
		"guest_data":    {{Key: "_id", Value: VMID}, {Key: "vm_uuid", Value: VMID}, {Key: "updated_at", Value: clock}, {Key: "entry", Value: bson.D{{Key: "updatedat", Value: clock}, {Key: "payload", Value: bson.D{{Key: "vmuuid", Value: VMID}, {Key: "hostname", Value: "composition-expired"}, {Key: "arch", Value: "amd64"}}}}}},
		"audit_events":  {{Key: "_id", Value: "composition-audit"}, {Key: "id", Value: "composition-audit"}, {Key: "at", Value: clock}, {Key: "actor_id", Value: "fixture-reader"}, {Key: "action", Value: "composition.seed"}, {Key: "success", Value: true}},
		"api_tokens":    {{Key: "_id", Value: "composition-token"}, {Key: "id", Value: "composition-token"}, {Key: "name", Value: "inert composition token"}, {Key: "ownerid", Value: "fixture-reader"}, {Key: "kind", Value: "user"}, {Key: "hash", Value: hash([]byte("synthetic-not-a-bearer"))}, {Key: "createdat", Value: clock}, {Key: "revoked", Value: true}},
		"sessions":      {{Key: "_id", Value: "composition-session"}, {Key: "token", Value: "composition-session"}, {Key: "userid", Value: "fixture-reader"}, {Key: "createdat", Value: clock}, {Key: "expiresat", Value: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}},
		"alarms":        {{Key: "_id", Value: "composition-alarm"}, {Key: "id", Value: "composition-alarm"}, {Key: "name", Value: "composition disabled alarm"}, {Key: "enabled", Value: false}, {Key: "metric", Value: "host.cpu_percent"}, {Key: "threshold", Value: float64(80)}, {Key: "forseconds", Value: int32(0)}, {Key: "severity", Value: "warning"}, {Key: "hosts", Value: []string{HostID}}},
	}
	out := Bundle{Schema: CompositionSchema, Source: DeployedSource, Documents: map[string][]byte{}, Indexes: map[string][]byte{}, Checksums: map[string]string{}}
	for name, doc := range docs {
		raw := marshal(append(doc, bson.E{Key: "opaque", Value: opaque}))
		indexes := marshal(bson.D{{Key: "v", Value: int32(2)}, {Key: "name", Value: "_id_"}, {Key: "key", Value: bson.D{{Key: "_id", Value: int32(1)}}}})
		// Inert legacy metadata accompanies originals, never installed by conversion.
		if name == "guest_data" || name == "sessions" {
			key := "updated_at"
			ttl := int32(300)
			if name == "sessions" {
				key = "expires_at"
				ttl = 0
			}
			indexes = append(indexes, marshal(bson.D{{Key: "v", Value: int32(2)}, {Key: "name", Value: key + "_1"}, {Key: "key", Value: bson.D{{Key: key, Value: int32(1)}}}, {Key: "expireAfterSeconds", Value: ttl}})...)
		}
		out.Documents[name] = raw
		out.Indexes[name] = indexes
		out.Checksums[name+".bson"] = hash(raw)
		out.Checksums[name+".indexes.bson"] = hash(indexes)
	}
	// The converted affinity component references three real UUIDs. Keep the
	// required separate pair on different synthetic hosts; no agent addresses.
	for _, x := range []struct {
		collection string
		doc        bson.D
	}{
		{"hosts", bson.D{{Key: "_id", Value: "composition-peer"}, {Key: "id", Value: "composition-peer"}, {Key: "status", Value: "offline"}, {Key: "opaque", Value: opaque}}},
		{"vms", bson.D{{Key: "_id", Value: AffinityVM2}, {Key: "uuid", Value: AffinityVM2}, {Key: "host_id", Value: "composition-peer"}, {Key: "name", Value: "composition-peer-vm"}, {Key: "state", Value: "stopped"}, {Key: "opaque", Value: opaque}}},
		{"vms", bson.D{{Key: "_id", Value: AffinityVM3}, {Key: "uuid", Value: AffinityVM3}, {Key: "host_id", Value: HostID}, {Key: "name", Value: "composition-neighbour-vm"}, {Key: "state", Value: "stopped"}, {Key: "opaque", Value: opaque}}},
	} {
		out.Documents[x.collection] = append(out.Documents[x.collection], marshal(x.doc)...)
		out.Checksums[x.collection+".bson"] = hash(out.Documents[x.collection])
	}
	return out
}

func CompositionParts() map[string]Bundle {
	return map[string]Bundle{"entities": GenerateCompositionEntities(), "history": GenerateHistoryReader(), "affinity": GenerateAffinity(), "tags": GenerateTags()}
}

type CompositionField struct{ Path, BSONType, Treatment string }
type CompositionCollection struct {
	Part, Schema, Source, Collection, DataSHA256, IndexSHA256 string
	DocumentIDs                                               []string
	Fields                                                    []CompositionField
	Indexes                                                   []json.RawMessage
}

// CompositionAccounting enumerates actual persisted fields (including wrappers,
// arrays and opaque roots) and original index definitions. It grants no behavior.
func CompositionAccounting() ([]CompositionCollection, error) {
	var out []CompositionCollection
	for part, b := range CompositionParts() {
		for name, raw := range b.Documents {
			row := CompositionCollection{Part: part, Schema: b.Schema, Source: b.Source, Collection: name, DataSHA256: b.Checksums[name+".bson"], IndexSHA256: b.Checksums[name+".indexes.bson"]}
			seen := map[string]bool{}
			var walk func(bson.Raw, string) error
			walk = func(doc bson.Raw, prefix string) error {
				els, e := doc.Elements()
				if e != nil {
					return e
				}
				for _, el := range els {
					path := prefix + el.Key()
					typ := el.Value().Type.String()
					treatment := "typed component projection; only documented operations qualified"
					if name == "sessions" && path == "expiresat" {
						treatment = "explicit canonical alias conversion to expires_at; datetime bytes retained"
					}
					if strings.HasPrefix(path, "opaque") {
						treatment = "opaque byte preservation; no behavioral meaning"
					}
					if part != "entities" {
						treatment = "explicit pinned offline conversion; original BSON retained"
					}
					key := path + ":" + typ
					if !seen[key] {
						row.Fields = append(row.Fields, CompositionField{path, typ, treatment})
						seen[key] = true
					}
					if v, ok := el.Value().DocumentOK(); ok {
						if e = walk(v, path+"."); e != nil {
							return e
						}
					}
					if a, ok := el.Value().ArrayOK(); ok {
						values, e := a.Values()
						if e != nil {
							return e
						}
						for _, v := range values {
							key := path + "[]:" + v.Type.String()
							if !seen[key] {
								row.Fields = append(row.Fields, CompositionField{path + "[]", v.Type.String(), treatment})
								seen[key] = true
							}
							if d, ok := v.DocumentOK(); ok {
								if e = walk(d, path+"[]."); e != nil {
									return e
								}
							}
						}
					}
				}
				return nil
			}
			docs, e := splitDocuments(raw)
			if e != nil {
				return nil, e
			}
			for _, doc := range docs {
				row.DocumentIDs = append(row.DocumentIDs, doc.Lookup("_id").StringValue())
				if e = walk(doc, ""); e != nil {
					return nil, e
				}
			}
			indexes, e := splitDocuments(b.Indexes[name])
			if e != nil {
				return nil, e
			}
			for _, index := range indexes {
				j, e := bson.MarshalExtJSON(index, true, false)
				if e != nil {
					return nil, e
				}
				row.Indexes = append(row.Indexes, j)
			}
			sort.Strings(row.DocumentIDs)
			sort.Slice(row.Fields, func(i, j int) bool {
				if row.Fields[i].Path == row.Fields[j].Path {
					return row.Fields[i].BSONType < row.Fields[j].BSONType
				}
				return row.Fields[i].Path < row.Fields[j].Path
			})
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Part == out[j].Part {
			return out[i].Collection < out[j].Collection
		}
		return out[i].Part < out[j].Part
	})
	return out, nil
}

func splitDocuments(raw []byte) ([]bson.Raw, error) {
	var docs []bson.Raw
	for len(raw) > 0 {
		doc, rest, ok := bsoncore.ReadDocument(raw)
		if !ok || bson.Raw(doc).Validate() != nil {
			return nil, fmt.Errorf("invalid component BSON")
		}
		docs = append(docs, bson.Raw(doc))
		raw = rest
	}
	return docs, nil
}
