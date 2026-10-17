package models

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestCanonicalBSONCompatibility(t *testing.T) {
	at := time.Date(2099, 1, 2, 3, 4, 5, 678000000, time.UTC)
	specs := []struct {
		name, key, old string
		value, other   any
		new            func() any
		read           func(any) any
		zero           any
	}{
		{"VM", "host_id", "hostid", "fixture-host", "other-host", func() any { return &VM{HostID: "sentinel"} }, func(v any) any { return v.(*VM).HostID }, ""},
		{"Session", "expires_at", "expiresat", at, at.Add(time.Millisecond), func() any { return &Session{ExpiresAt: at.Add(time.Hour)} }, func(v any) any { return v.(*Session).ExpiresAt }, time.Time{}},
		{"Migration", "started_at", "startedat", int64(1792108800), int64(1792108801), func() any { return &MigrationRecord{StartedAt: 99} }, func(v any) any { return v.(*MigrationRecord).StartedAt }, int64(0)},
		{"Audit", "actor_id", "actorid", "fixture-actor", "other-actor", func() any { return &AuditEvent{ActorID: "sentinel"} }, func(v any) any { return v.(*AuditEvent).ActorID }, ""},
	}
	for _, s := range specs {
		t.Run(s.name, func(t *testing.T) {
			cases := []struct {
				name         string
				doc          bson.D
				bad, missing bool
			}{
				{"canonical", bson.D{{Key: s.key, Value: s.value}}, false, false},
				{"old", bson.D{{Key: s.old, Value: s.value}}, false, false},
				{"equal", bson.D{{Key: s.key, Value: s.value}, {Key: s.old, Value: s.value}}, false, false},
				{"equal_reversed", bson.D{{Key: s.old, Value: s.value}, {Key: s.key, Value: s.value}}, false, false},
				{"conflict", bson.D{{Key: s.key, Value: s.value}, {Key: s.old, Value: s.other}}, true, false},
				{"conflict_reversed", bson.D{{Key: s.old, Value: s.other}, {Key: s.key, Value: s.value}}, true, false},
				{"null", bson.D{{Key: s.key, Value: nil}}, true, false},
				{"old_null", bson.D{{Key: s.old, Value: nil}}, true, false},
				{"null_and_valid", bson.D{{Key: s.old, Value: nil}, {Key: s.key, Value: s.value}}, true, false},
				{"array", bson.D{{Key: s.key, Value: bson.A{}}}, true, false},
				{"document", bson.D{{Key: s.key, Value: bson.D{}}}, true, false},
				{"duplicate", bson.D{{Key: s.key, Value: s.value}, {Key: s.key, Value: s.value}}, true, false},
				{"old_duplicate", bson.D{{Key: s.old, Value: s.value}, {Key: s.old, Value: s.value}}, true, false},
				{"case_variant", bson.D{{Key: strings.ToUpper(s.key), Value: s.value}}, true, false},
				{"old_case_variant", bson.D{{Key: strings.ToUpper(s.old), Value: s.value}}, true, false},
				{"unrelated_duplicate", bson.D{{Key: "name", Value: "one"}, {Key: "name", Value: "two"}}, true, false},
				{"missing", bson.D{}, false, true},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					raw, err := bson.Marshal(c.doc)
					if err != nil {
						t.Fatal(err)
					}
					original := append([]byte(nil), raw...)
					target := s.new()
					before := reflect.ValueOf(target).Elem().Interface()
					err = bson.Unmarshal(raw, target)
					if !bytes.Equal(raw, original) {
						t.Fatal("source BSON mutated")
					}
					if c.bad {
						if err == nil {
							t.Fatal("ambiguous/invalid BSON accepted")
						}
						if !reflect.DeepEqual(before, reflect.ValueOf(target).Elem().Interface()) {
							t.Fatal("failed decode changed receiver")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					want := s.value
					if c.missing {
						want = s.zero
					}
					if !reflect.DeepEqual(s.read(target), want) {
						t.Fatalf("decoded value mismatch: got%v want%v", s.read(target), want)
					}
					encoded, err := bson.Marshal(target)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := bson.Raw(encoded).LookupErr(s.key); err != nil {
						t.Fatal("canonical output missing")
					}
					if _, err := bson.Raw(encoded).LookupErr(s.old); err == nil {
						t.Fatal("old alias emitted")
					}
					again := s.new()
					if err := bson.Unmarshal(encoded, again); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(s.read(again), want) {
						t.Fatal("roundtrip changed value")
					}
					second, err := bson.Marshal(again)
					if err != nil || !bytes.Equal(second, encoded) {
						t.Fatal("canonical roundtrip not idempotent")
					}
				})
			}
			t.Run("malformed_atomic", func(t *testing.T) {
				target := s.new()
				before := reflect.ValueOf(target).Elem().Interface()
				if err := bson.Unmarshal([]byte{5, 0, 0}, target); err == nil {
					t.Fatal("truncated BSON accepted")
				}
				if !reflect.DeepEqual(before, reflect.ValueOf(target).Elem().Interface()) {
					t.Fatal("malformed decode changed receiver")
				}
			})
		})
	}
}

func TestCanonicalBSONExactScalarTypes(t *testing.T) {
	for _, value := range []any{int32(42), float64(42), "42", time.Unix(42, 0)} {
		raw, _ := bson.Marshal(bson.D{{Key: "started_at", Value: value}})
		if err := bson.Unmarshal(raw, &MigrationRecord{}); err == nil {
			t.Fatal("coerced migration timestamp")
		}
	}
	for _, value := range []any{int64(42), "2099-01-01T00:00:00Z", float64(42)} {
		raw, _ := bson.Marshal(bson.D{{Key: "expires_at", Value: value}})
		if err := bson.Unmarshal(raw, &Session{}); err == nil {
			t.Fatal("coerced session date")
		}
	}
	for _, key := range []string{"host_id", "actor_id"} {
		raw, _ := bson.Marshal(bson.D{{Key: key, Value: int64(42)}})
		var target any = &VM{}
		if key == "actor_id" {
			target = &AuditEvent{}
		}
		if err := bson.Unmarshal(raw, target); err == nil {
			t.Fatal("coerced identity")
		}
	}
	// Millisecond dates and historical zero/empty values retain exact semantics.
	for _, at := range []primitive.DateTime{-1, 0, 1, 4071000000678} {
		raw, _ := bson.Marshal(bson.D{{Key: "expiresat", Value: at}})
		var s Session
		if err := bson.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		if s.ExpiresAt.UnixMilli() != int64(at) {
			t.Fatal("date precision changed")
		}
	}
	raw, _ := bson.Marshal(bson.D{{Key: "actor_id", Value: ""}, {Key: "actorid", Value: ""}})
	if err := bson.Unmarshal(raw, &AuditEvent{}); err != nil {
		t.Fatal("explicit empty historical actor refused")
	}
}

func TestCanonicalBSONRetainsUnrelatedElementsInAdapter(t *testing.T) {
	raw, _ := bson.Marshal(bson.D{{Key: "opaque", Value: bson.D{{Key: "x", Value: int32(1)}}}, {Key: "hostid", Value: "fixture"}, {Key: "binary", Value: primitive.Binary{Subtype: 128, Data: []byte{0, 1, 255}}}})
	source := append([]byte(nil), raw...)
	got, err := canonicalBSONField(raw, "host_id", "hostid", bsontype.String)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"opaque", "binary"} {
		a, b := bson.Raw(raw).Lookup(key), bson.Raw(got).Lookup(key)
		if a.Type != b.Type || !bytes.Equal(a.Value, b.Value) {
			t.Fatal("adapter rewrote unrelated value")
		}
	}
	if !bytes.Equal(raw, source) {
		t.Fatal("adapter mutated source")
	}
	// This adapter preservation is NOT a claim that typed model Save retains
	// unknown fields. Full-schema conversion remains blocked.
}
