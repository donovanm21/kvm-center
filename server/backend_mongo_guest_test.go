package main

import (
	"go.mongodb.org/mongo-driver/bson"
	"testing"
	"time"
)

func TestMongoGuestDocumentPreservesNestedLegacyEnvelope(t *testing.T) {
	stamp := time.Date(2024, 1, 1, 0, 0, 0, 123000000, time.UTC)
	// Hand-written saved BSON shape: root TTL date and nested guest payload are
	// separate. Do not serialize through the type being tested.
	raw, err := bson.Marshal(bson.D{{Key: "vm_uuid", Value: "synthetic-vm"}, {Key: "updated_at", Value: stamp}, {Key: "entry", Value: bson.D{{Key: "updatedat", Value: stamp}, {Key: "payload", Value: bson.D{{Key: "vmuuid", Value: "synthetic-vm"}, {Key: "hostname", Value: "synthetic-guest"}, {Key: "memtotalmb", Value: int64(4096)}, {Key: "timestamp", Value: stamp}, {Key: "ips", Value: bson.A{"192.0.2.40"}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var got entityGuestReport
	if err := bson.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.UUID != "synthetic-vm" || got.Entry.Payload.VMUUID != got.UUID || got.Entry.Payload.Hostname != "synthetic-guest" || got.Entry.Payload.MemTotalMB != 4096 || !got.Entry.UpdatedAt.Equal(stamp) || !got.Entry.Payload.Timestamp.Equal(stamp) || len(got.Entry.Payload.IPs) != 1 {
		t.Fatalf("nested payload lost: %#v", got)
	}
}
