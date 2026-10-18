package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"kvm-center/internal/models"
	"testing"
	"time"
)

func auditFixture(t *testing.T, id string) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(bson.D{{Key: "_id", Value: id}, {Key: "id", Value: id}, {Key: "at", Value: time.Date(2020, 1, 2, 3, 4, 5, 123000000, time.UTC)}, {Key: "actor_id", Value: "synthetic-actor"}, {Key: "actorname", Value: "Synthetic actor"}, {Key: "actorip", Value: "synthetic-address"}, {Key: "action", Value: "legacy.event"}, {Key: "resource", Value: "synthetic-resource"}, {Key: "detail", Value: bson.D{{Key: "note", Value: "retained"}}}, {Key: "success", Value: false}, {Key: "error", Value: "synthetic failure"}, {Key: "opaque", Value: primitive.NewDecimal128(83, 89)}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestAuditPreservationWire(t *testing.T) {
	raw := auditFixture(t, "original")
	if err := validateAuditBSON(raw); err != nil {
		t.Fatal(err)
	}
	var ev models.AuditEvent
	if err := bson.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	d := &persistData{Audit: []*models.AuditEvent{&ev}, EntityOverlay: &entityOverlay{Version: 5, Collections: map[string]map[string][]byte{mcoAuditEvents: {ev.ID: raw}}}}
	wire, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back persistData
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatal(err)
	}
	if !equalRaw(bson.Raw(back.EntityOverlay.Collections[mcoAuditEvents][ev.ID]).Lookup("opaque"), raw.Lookup("opaque")) {
		t.Fatal("opaque value lost")
	}
	old := bytes.Replace(wire, []byte(`"version":5`), []byte(`"version":4`), 1)
	if json.Unmarshal(old, &back) == nil {
		t.Fatal("old overlay accepted audit")
	}
	for _, events := range [][]*models.AuditEvent{{nil}, {&ev, &ev}, {{ID: "", At: ev.At}}, {{ID: "zero"}}} {
		if _, err := json.Marshal(&persistData{Audit: events}); err == nil {
			t.Fatal("ambiguous snapshot accepted")
		}
	}
}
func TestAuditChronologicalRetentionAndCapacity(t *testing.T) {
	s := NewStoreWithBackend(nil, t.TempDir()+"/state.json")
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	// Keep persistence inert: this test exercises the active ring, not disk I/O.
	for i := auditRetentionMax; i >= 0; i-- {
		s.AddAuditEvent(&models.AuditEvent{ID: fmt.Sprintf("event-%05d", i), At: base.Add(time.Duration(i/2) * time.Second), Action: "synthetic"})
	}
	s.saveWG.Wait()
	events, total := s.GetAuditEvents(AuditFilter{Limit: auditRetentionMax + 1})
	if total != auditRetentionMax || events[0].ID != "event-10000" || events[len(events)-1].ID != "event-00001" {
		t.Fatal("retention/order differs from chronological ID tie policy")
	}
	s.AddAuditEvent(&models.AuditEvent{ID: "event-10000", At: base, Action: "must-not-replace"})
	events, _ = s.GetAuditEvents(AuditFilter{Limit: 1})
	if events[0].Action != "synthetic" {
		t.Fatal("duplicate changed original")
	}
	d := &persistData{Audit: append([]*models.AuditEvent(nil), s.audit...), Groups: map[string]*models.Group{"g": {ID: "g"}}, EntityOverlay: &entityOverlay{Version: 5, Collections: map[string]map[string][]byte{mcoAuditEvents: {"event-00000": auditFixture(t, "event-00000")}}}}
	wire, err := json.Marshal(d)
	if err != nil {
		t.Fatal("full audit ring plus inventory cannot be backed up", err)
	}
	var back persistData
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatal(err)
	}
	if _, ok := back.EntityOverlay.Collections[mcoAuditEvents]["event-00000"]; ok {
		t.Fatal("evicted audit resurrected")
	}
	if len(back.Audit) != auditRetentionMax || len(back.EntityOverlay.Collections[mcoGroups]) != 1 {
		t.Fatal("capacity roundtrip lost records")
	}
	oversized := append(back.Audit, &models.AuditEvent{ID: "extra", At: base})
	if validateAuditSnapshot(oversized) == nil {
		t.Fatal("oversized admission accepted")
	}
}

func TestAuditStrictBSONAdmission(t *testing.T) {
	original := auditFixture(t, "original")
	for _, kind := range []string{"identity", "missing-at", "null-at", "alias", "alias-case", "detail-type", "duplicate", "case"} {
		t.Run(kind, func(t *testing.T) {
			var doc bson.D
			bson.Unmarshal(original, &doc)
			switch kind {
			case "identity":
				doc[0].Value = "other"
			case "missing-at":
				for i := range doc {
					if doc[i].Key == "at" {
						doc = append(doc[:i], doc[i+1:]...)
						break
					}
				}
			case "null-at":
				for i := range doc {
					if doc[i].Key == "at" {
						doc[i].Value = nil
					}
				}
			case "alias-case":
				doc = append(doc, bson.E{Key: "ACTORID", Value: "conflict"})
			case "alias":
				doc = append(doc, bson.E{Key: "actorid", Value: "conflict"})
			case "detail-type":
				for i := range doc {
					if doc[i].Key == "detail" {
						doc[i].Value = bson.D{{Key: "number", Value: int32(4)}}
					}
				}
			case "duplicate":
				doc = append(doc, bson.E{Key: "action", Value: "other"})
			case "case":
				doc = append(doc, bson.E{Key: "ActorName", Value: "other"})
			}
			raw, _ := bson.Marshal(doc)
			if validateAuditBSON(raw) == nil {
				t.Fatal("ambiguous audit accepted")
			}
		})
	}
}

func TestAuditAliasCaseOnlyAndConflict(t *testing.T) {
	for _, pair := range [][2]string{{"actor_id", "ACTORID"}, {"actorname", "ACTOR_NAME"}, {"actorip", "ACTOR_IP"}} {
		for _, conflict := range []bool{false, true} {
			var doc bson.D
			bson.Unmarshal(auditFixture(t, "original"), &doc)
			if !conflict {
				for i := range doc {
					if doc[i].Key == pair[0] {
						doc = append(doc[:i], doc[i+1:]...)
						break
					}
				}
			}
			doc = append(doc, bson.E{Key: pair[1], Value: "ambiguous"})
			raw, _ := bson.Marshal(doc)
			if validateAuditBSON(raw) == nil {
				t.Fatal("mixed case alias accepted", pair, conflict)
			}
		}
	}
}

func TestAuditLegacyAliasAndRawMapOrder(t *testing.T) {
	var doc bson.D
	bson.Unmarshal(auditFixture(t, "original"), &doc)
	for i := range doc {
		if doc[i].Key == "actor_id" {
			doc[i].Key = "actorid"
		}
		if doc[i].Key == "detail" {
			doc[i].Value = bson.D{{Key: "z", Value: "last"}, {Key: "a", Value: "first"}, {Key: "m", Value: "middle"}}
		}
	}
	original, _ := bson.Marshal(doc)
	if err := validateAuditBSON(original); err != nil {
		t.Fatal("strict historical alias refused", err)
	}
	var event models.AuditEvent
	if err := bson.Unmarshal(original, &event); err != nil {
		t.Fatal(err)
	}
	overlay := &entityOverlay{Version: 5, Collections: map[string]map[string][]byte{mcoAuditEvents: {event.ID: original}}}
	for i := 0; i < 30; i++ {
		d := &persistData{Audit: []*models.AuditEvent{&event}, EntityOverlay: overlay}
		wire, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var back persistData
		if err := json.Unmarshal(wire, &back); err != nil {
			t.Fatal(err)
		}
		raw := bson.Raw(back.EntityOverlay.Collections[mcoAuditEvents][event.ID])
		if raw.Lookup("actorid").Type != 0 || raw.Lookup("actor_id").StringValue() != event.ActorID || !equalRaw(raw.Lookup("detail"), bson.Raw(original).Lookup("detail")) {
			t.Fatal("alias/map preservation changed")
		}
		overlay = back.EntityOverlay
	}
}
