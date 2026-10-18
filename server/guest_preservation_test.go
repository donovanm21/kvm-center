package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
)

func guestFixture(t *testing.T, nestedOpaque bool) bson.Raw {
	t.Helper()
	raw := bson.Raw(legacyfixture.GenerateDeployed().Documents[mcoGuestData])
	var doc bson.D
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc = append(doc, bson.E{Key: "opaque", Value: primitive.NewDecimal128(59, 61)})
	if nestedOpaque {
		for i := range doc {
			if doc[i].Key == "entry" {
				doc[i].Value = append(doc[i].Value.(bson.D), bson.E{Key: "opaque", Value: primitive.Binary{Subtype: 0x80, Data: []byte{0, 255}}})
			}
		}
	}
	out, err := bson.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGuestPreservationExactLegacyNestedLayout(t *testing.T) {
	raw := guestFixture(t, false)
	if err := validateGuestRecord(raw); err != nil {
		t.Fatal(err)
	}
	var doc entityGuestReport
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Entry.Payload.Arch != "synthetic-arch" || doc.Entry.Payload.GrowResult == nil || doc.Entry.Payload.GrowResult.Device != "synthetic-device" || doc.Entry.Payload.GrowResult.Result != "synthetic-result" || doc.Entry.Payload.GrowResult.Error != "synthetic-error" {
		t.Fatal("deployed guest fields lost")
	}
	original := doc.Entry
	d := &persistData{GuestData: map[string]*guestEntry{doc.UUID: &doc.Entry}, EntityOverlay: &entityOverlay{Version: 4, Collections: map[string]map[string][]byte{mcoGuestData: {doc.UUID: raw}}}}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back persistData
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*back.GuestData[doc.UUID], original) {
		t.Fatal("guest JSON projection changed")
	}
	// Expired cache timestamps remain expired through backup, without refreshing.
	if !back.GuestData[doc.UUID].UpdatedAt.Before(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("expired report refreshed")
	}
	back.GuestData[doc.UUID].Payload.Hostname = "edited"
	back.GuestData[doc.UUID].Payload.GrowResult = nil // A later report may clear its one-shot result.
	next, err := reconcileOverlay(&back, back.EntityOverlay)
	if err != nil {
		t.Fatal(err)
	}
	got := bson.Raw(next.Collections[mcoGuestData][doc.UUID])
	if !equalRaw(raw.Lookup("opaque"), got.Lookup("opaque")) || got.Lookup("entry", "payload", "hostname").StringValue() != "edited" || got.Lookup("entry", "payload", "growresult").Type != 10 {
		t.Fatal("guest edit or opaque value lost")
	}
	for version := 1; version <= 3; version++ {
		next.Version = version
		if next.validate() == nil {
			t.Fatal("guest accepted under old overlay version")
		}
	}
}

func TestGuestPreservationOpaqueEntryRefusesEdits(t *testing.T) {
	raw := guestFixture(t, true)
	var doc entityGuestReport
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	d := &persistData{GuestData: map[string]*guestEntry{doc.UUID: &doc.Entry}}
	candidate, _ := bson.Marshal(overlayCandidates(d)[mcoGuestData][0])
	got, err := preserveEntityBSON(raw, candidate, reflect.TypeOf(doc), nil)
	if err != nil || !equalRaw(raw.Lookup("entry"), bson.Raw(got).Lookup("entry")) {
		t.Fatal("unchanged opaque entry lost", err)
	}
	doc.Entry.Payload.Hostname = "changed"
	candidate, _ = bson.Marshal(overlayCandidates(d)[mcoGuestData][0])
	if _, err := preserveEntityBSON(raw, candidate, reflect.TypeOf(doc), nil); err == nil {
		t.Fatal("opaque entry edit accepted")
	}
}

func TestGuestRecordRefusesAmbiguousShapesAndTypes(t *testing.T) {
	for name, change := range map[string]func(bson.M){
		"flattened": func(d bson.M) {
			entry := d["entry"].(bson.M)
			delete(d, "entry")
			d["payload"] = entry["payload"]
			d["updatedat"] = entry["updatedat"]
		},
		"null entry":         func(d bson.M) { d["entry"] = nil },
		"null payload":       func(d bson.M) { d["entry"].(bson.M)["payload"] = nil },
		"mismatch":           func(d bson.M) { d["vm_uuid"] = "other" },
		"spaced identity":    func(d bson.M) { d["_id"] = " " + d["_id"].(string) },
		"timestamp conflict": func(d bson.M) { d["updated_at"] = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) },
		"timestamp absent":   func(d bson.M) { delete(d, "updated_at") },
		"timestamp string":   func(d bson.M) { d["updated_at"] = "2024-01-01" },
		"root alias":         func(d bson.M) { d["updatedat"] = d["updated_at"] },
		"entry alias":        func(d bson.M) { d["entry"].(bson.M)["updated_at"] = d["updated_at"] },
		"payload alias":      func(d bson.M) { d["entry"].(bson.M)["payload"].(bson.M)["grow_result"] = bson.M{} },
		"arch type":          func(d bson.M) { d["entry"].(bson.M)["payload"].(bson.M)["arch"] = int32(7) },
		"grow type":          func(d bson.M) { d["entry"].(bson.M)["payload"].(bson.M)["growresult"] = bson.A{} },
	} {
		t.Run(name, func(t *testing.T) {
			var d bson.M
			bson.Unmarshal(guestFixture(t, false), &d)
			change(d)
			raw, err := bson.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			if validateGuestRecord(raw) == nil {
				t.Fatal("ambiguous guest accepted")
			}
		})
	}
	raw := guestFixture(t, false)
	var doc bson.D
	bson.Unmarshal(raw, &doc)
	doc = append(doc, bson.E{Key: "vm_uuid", Value: legacyfixture.VMID})
	dup, _ := bson.Marshal(doc)
	if validateGuestRecord(dup) == nil {
		t.Fatal("duplicate field accepted")
	}
	d := &persistData{GuestData: map[string]*guestEntry{"wrong": {Payload: models.GuestTelemetryPayload{VMUUID: legacyfixture.VMID}}}}
	if validateEntitySnapshot(d) == nil {
		t.Fatal("snapshot identity mismatch accepted")
	}
}

func guestArchivedOriginal(t *testing.T) bson.Raw {
	t.Helper()
	var doc bson.D
	if err := bson.Unmarshal(guestFixture(t, true), &doc); err != nil {
		t.Fatal(err)
	}
	const id = "22222222-2222-4222-8222-222222222222"
	for i := range doc {
		switch doc[i].Key {
		case "_id", "vm_uuid":
			doc[i].Value = id
		case "entry":
			entry := doc[i].Value.(bson.D)
			for j := range entry {
				if entry[j].Key == "payload" {
					payload := entry[j].Value.(bson.D)
					for k := range payload {
						if payload[k].Key == "vmuuid" {
							payload[k].Value = id
						}
					}
					entry[j].Value = payload
				}
			}
			doc[i].Value = entry
		}
	}
	raw, err := bson.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestGuestTelemetryRefusesOpaqueMutationBeforeMemory(t *testing.T) {
	raw := guestFixture(t, true)
	var report entityGuestReport
	if err := bson.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	vm := &models.VM{UUID: report.UUID, HostID: "fixture", GuestHostname: "unchanged"}
	s := &Store{guestData: map[string]*guestEntry{report.UUID: &report.Entry}, vms: map[string][]*models.VM{"fixture": {vm}}, entityOverlay: &entityOverlay{Version: 4, Collections: map[string]map[string][]byte{mcoGuestData: {report.UUID: raw}}}}
	body, _ := json.Marshal(models.GuestTelemetryPayload{VMUUID: report.UUID, Hostname: "rejected"})
	req := httptest.NewRequest("POST", "/api/guest-telemetry", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handleGuestTelemetry(s)(response, req)
	if response.Code != 409 || vm.GuestHostname != "unchanged" || s.guestData[report.UUID].Payload.Hostname == "rejected" || !s.guestData[report.UUID].UpdatedAt.Equal(report.Entry.UpdatedAt) {
		t.Fatal("refused report mutated visible state")
	}
	d := &persistData{GuestData: s.guestData, EntityOverlay: s.entityOverlay, Groups: map[string]*models.Group{"unrelated": {ID: "unrelated", Description: "allowed edit"}}}
	if _, err := json.Marshal(d); err != nil {
		t.Fatal("refusal poisoned unrelated persistence", err)
	}
}
