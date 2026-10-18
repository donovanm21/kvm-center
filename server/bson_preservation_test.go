package main

import (
	"bytes"
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
)

func TestBSONPreservationLegacyEntityFields(t *testing.T) {
	f := legacyfixture.GenerateDeployed()
	for collection, entity := range map[string]reflect.Type{mcoHosts: reflect.TypeOf(models.Host{}), mcoVMs: reflect.TypeOf(models.VM{})} {
		t.Run(collection, func(t *testing.T) {
			original := bson.Raw(f.Documents[collection])
			before := append([]byte(nil), original...)
			model := reflect.New(entity)
			if err := bson.Unmarshal(original, model.Interface()); err != nil {
				t.Fatal(err)
			}
			model.Elem().FieldByName("CPUUsage").SetFloat(42.5)
			raw, err := bson.Marshal(model.Interface())
			if err != nil {
				t.Fatal(err)
			}
			es, _ := bson.Raw(raw).Elements()
			d := bson.D{{Key: "_id", Value: original.Lookup("_id")}}
			for _, e := range es {
				d = append(d, bson.E{Key: e.Key(), Value: e.Value()})
			}
			candidate, _ := bson.Marshal(d)
			merged, err := preserveEntityBSON(original, candidate, entity, map[string]string{"hostid": "host_id"})
			if err != nil {
				t.Fatal(err)
			}
			es, _ = original.Elements()
			for _, e := range es {
				if e.Key() == "cpuusage" {
					continue
				}
				if !equalRaw(e.Value(), merged.Lookup(e.Key())) {
					t.Fatalf("field lost or changed: %s", e.Key())
				}
			}
			if merged.Lookup("cpuusage").Double() != 42.5 || !bytes.Equal(original, before) {
				t.Fatal("write or original preservation failed")
			}
		})
	}
}

func TestBSONPreservationOpaqueContainersAndRefusals(t *testing.T) {
	base := bson.D{{Key: "_id", Value: "vm"}, {Key: "uuid", Value: "vm"}, {Key: "hostid", Value: "host"},
		{Key: "networks", Value: bson.A{bson.D{{Key: "mac", Value: "synthetic-mac"}, {Key: "opaque", Value: primitive.Binary{Subtype: 0x80, Data: []byte{1, 0, 255}}}}}},
		{Key: "unknown", Value: primitive.NewDecimal128(7, 9)}}
	old, _ := bson.Marshal(base)
	var v models.VM
	if err := bson.Unmarshal(old, &v); err != nil {
		t.Fatal(err)
	}
	encode := func(v models.VM) bson.Raw {
		r, err := bson.Marshal(struct {
			ID string    `bson:"_id"`
			VM models.VM `bson:",inline"`
		}{"vm", v})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	v.Notes = "edited"
	merged, err := preserveEntityBSON(old, encode(v), reflect.TypeOf(v), map[string]string{"hostid": "host_id"})
	if err != nil {
		t.Fatal(err)
	}
	if !equalRaw(merged.Lookup("networks"), bson.Raw(old).Lookup("networks")) || !equalRaw(merged.Lookup("unknown"), bson.Raw(old).Lookup("unknown")) || merged.Lookup("hostid").Type != 0 || merged.Lookup("host_id").StringValue() != "host" {
		t.Fatal("opaque retention or canonical alias failed")
	}
	for _, mode := range []string{"nested edit", "remove container", "duplicate", "case collision", "type drift", "alias conflict"} {
		t.Run(mode, func(t *testing.T) {
			source := append(bson.D(nil), base...)
			next := v
			switch mode {
			case "nested edit":
				next.Networks = append([]models.VMNetwork(nil), v.Networks...)
				next.Networks[0].MAC = "changed"
			case "remove container":
				next.Networks = nil
			case "duplicate":
				source = append(source, bson.E{Key: "uuid", Value: "vm"})
			case "case collision":
				source = append(source, bson.E{Key: "UUID", Value: "vm"})
			case "type drift":
				source = append(source, bson.E{Key: "cpucount", Value: int64(7)})
			case "alias conflict":
				source = append(source, bson.E{Key: "host_id", Value: "other"})
			}
			raw, _ := bson.Marshal(source)
			if _, err := preserveEntityBSON(raw, encode(next), reflect.TypeOf(v), map[string]string{"hostid": "host_id"}); err == nil {
				t.Fatal("ambiguous/lossy mutation accepted")
			}
		})
	}
}
