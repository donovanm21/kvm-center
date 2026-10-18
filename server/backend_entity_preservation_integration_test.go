//go:build integration

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/legacyfixture"
)

func TestMongoIntegrationEntityRawPreservation(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f := legacyfixture.GenerateDeployed()
	for _, name := range []string{mcoHosts, mcoVMs} {
		if _, err := b.db.Collection(name).InsertOne(ctx, bson.Raw(f.Documents[name])); err != nil {
			t.Fatal(err)
		}
	}
	data, err := b.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data.Hosts[legacyfixture.HostID].CPUUsage = 41.25
	data.VMs[legacyfixture.HostID][0].Notes = "synthetic write"
	if err := b.Save(ctx, data); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := b.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Hosts[legacyfixture.HostID].CPUUsage != 41.25 || again.VMs[legacyfixture.HostID][0].Notes != "synthetic write" {
		t.Fatal("known writes lost after reload")
	}
	for _, name := range []string{mcoHosts, mcoVMs} {
		var got bson.Raw
		if err := b.db.Collection(name).FindOne(ctx, bson.D{}).Decode(&got); err != nil {
			t.Fatal(err)
		}
		es, _ := bson.Raw(f.Documents[name]).Elements()
		for _, e := range es {
			if (name == mcoHosts && e.Key() == "cpuusage") || (name == mcoVMs && e.Key() == "notes") {
				continue
			}
			if !equalRaw(e.Value(), got.Lookup(e.Key())) {
				t.Fatalf("lost field %s.%s", name, e.Key())
			}
		}
	}
}

func TestMongoIntegrationEntityPreflightBeforeWrites(t *testing.T) {
	for _, mode := range []string{"nested edit", "duplicate identity"} {
		t.Run(mode, func(t *testing.T) {
			b := newMongoIntegrationBackend(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			host, _ := bson.Marshal(bson.D{{Key: "_id", Value: "host"}, {Key: "id", Value: "host"}, {Key: "cpuusage", Value: 1.0}})
			vm, _ := bson.Marshal(bson.D{{Key: "_id", Value: "vm"}, {Key: "uuid", Value: "vm"}, {Key: "host_id", Value: "host"}, {Key: "networks", Value: bson.A{bson.D{{Key: "mac", Value: "old"}, {Key: "opaque", Value: int64(9)}}}}})
			for name, raw := range map[string][]byte{mcoHosts: host, mcoVMs: vm} {
				if _, err := b.db.Collection(name).InsertOne(ctx, bson.Raw(raw)); err != nil {
					t.Fatal(err)
				}
			}
			data, err := b.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			data.Hosts["host"].CPUUsage = 99
			if mode == "nested edit" {
				data.VMs["host"][0].Networks[0].MAC = "changed"
			} else {
				data.VMs["host"] = append(data.VMs["host"], data.VMs["host"][0])
			}
			if err := b.Save(ctx, data); err == nil {
				t.Fatal("ambiguous mutation accepted")
			}
			for name, raw := range map[string][]byte{mcoHosts: host, mcoVMs: vm} {
				var got bson.Raw
				if err := b.db.Collection(name).FindOne(ctx, bson.D{}).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(raw, got) {
					t.Fatal("preflight refusal wrote data")
				}
			}
		})
	}
}
