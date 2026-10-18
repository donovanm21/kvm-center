package affinitypolicy

import (
	"bytes"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"testing"
)

const a = "11111111-1111-4111-8111-111111111111"
const b = "22222222-2222-4222-8222-222222222222"
const c = "33333333-3333-4333-8333-333333333333"

func rule(id, kind, strength string, members ...string) []byte {
	v, _ := bson.Marshal(bson.D{{Key: "_id", Value: id}, {Key: "id", Value: id}, {Key: "name", Value: id}, {Key: "enabled", Value: true}, {Key: "kind", Value: kind}, {Key: "strength", Value: strength}, {Key: "vms", Value: members}})
	return v
}
func fixture() *State {
	return &State{Version: 1, SourceContract: Source, Originals: map[string][]byte{"separate": rule("separate", "separate", "must", a, b)}}
}
func TestPlacementPolicy(t *testing.T) {
	in := []Placement{{a, "one", "source"}, {b, "two", "peer"}, {c, "three", "source"}}
	q := Reservation{RequestID: "request", UUID: a, Name: "one", Source: "source", Destination: "target", DestinationAddress: "192.0.2.3"}
	s := fixture()
	if _, e := s.Check(in, q); e != nil {
		t.Fatal(e)
	}
	q.Destination = "peer"
	if _, e := s.Check(in, q); e == nil {
		t.Fatal("required rule allowed")
	}
	q.Destination = "target"
	s.Originals["preference"] = rule("preference", "together", "should", a, c)
	w, e := s.Check(in, q)
	if e != nil || len(w) != 1 {
		t.Fatal(w, e)
	}
	for _, tc := range []struct {
		name string
		edit func(*State, []Placement, *Reservation)
	}{
		{name: "pending", edit: func(s *State, _ []Placement, q *Reservation) { r := *q; s.Reservation = &r }},
		{name: "conflicting name", edit: func(_ *State, _ []Placement, q *Reservation) { q.Name = "two" }},
		{name: "conflicting source", edit: func(_ *State, _ []Placement, q *Reservation) { q.Source = "peer" }},
		{name: "duplicate uuid", edit: func(_ *State, p []Placement, _ *Reservation) { p[1].UUID = a }},
		{name: "duplicate name", edit: func(_ *State, p []Placement, _ *Reservation) { p[1].Name = "ONE" }},
		{name: "crossmatch", edit: func(_ *State, p []Placement, _ *Reservation) { p[1].Name = a }},
		{name: "unknown member", edit: func(_ *State, p []Placement, _ *Reservation) { p[1].UUID = "44444444-4444-4444-8444-444444444444" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture()
			p := append([]Placement(nil), in...)
			r := q
			tc.edit(s, p, &r)
			if _, e := s.Check(p, r); e == nil {
				t.Fatal("unsafe placement allowed")
			}
		})
	}
}
func TestTransitiveContradictions(t *testing.T) {
	s := fixture()
	s.Originals["ab"] = rule("ab", "together", "must", a, c)
	s.Originals["bc"] = rule("bc", "together", "must", c, b)
	if s.Validate() == nil {
		t.Fatal("transitive contradiction accepted")
	}
}
func TestStrictOriginalAndState(t *testing.T) {
	s := fixture()
	original := append([]byte(nil), s.Originals["separate"]...)
	q := Reservation{RequestID: "request", UUID: a, Name: "one", Source: "source", Destination: "target", DestinationAddress: "192.0.2.3"}
	s.Reservation = &q
	raw, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	var restored State
	if e = json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(original, restored.Originals["separate"]) || *restored.Reservation != q {
		t.Fatal("recovery lost original/reservation")
	}
	for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"version"`), []byte(`"Version"`), 1), bytes.Replace(raw, []byte(`"request_id"`), []byte(`"Request_id"`), 1), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1), bytes.Replace(raw, []byte(`"version":1`), []byte(`"extra":true,"version":1`), 1)} {
		if json.Unmarshal(bad, &restored) == nil {
			t.Fatal("unsupported state accepted")
		}
	}
	for _, members := range [][]string{{a, "name"}, {a, "tag:database"}, {a, a}, {a}} {
		if _, e := Decode(rule("bad", "separate", "must", members...)); e == nil {
			t.Fatal("unsupported selector accepted")
		}
	}
	var d bson.D
	_ = bson.Unmarshal(original, &d)
	for _, extra := range []bson.E{{Key: "id", Value: "conflicting"}, {Key: "future", Value: true}, {Key: "created_at", Value: "alias"}, {Key: "enabled", Value: "true"}} {
		bad, _ := bson.Marshal(append(append(bson.D{}, d...), extra))
		if _, e := Decode(bad); e == nil {
			t.Fatal("ambiguous field accepted")
		}
	}
}

func TestRestoredOriginalRejectsMalformedArray(t *testing.T) {
	raw := rule("bad", "separate", "must", a, b)
	raw = bytes.Replace(raw, []byte{2, '1', 0}, []byte{2, '0', 0}, 1)
	if _, err := Decode(raw); err == nil {
		t.Fatal("duplicate BSON array key accepted")
	}
	s := &State{Version: 1, SourceContract: Source, Originals: map[string][]byte{"bad": raw}}
	encoded, _ := json.Marshal(wire(*s))
	var restored State
	if json.Unmarshal(encoded, &restored) == nil {
		t.Fatal("restored malformed BSON accepted")
	}
}

func TestOriginalExactBSONFraming(t *testing.T) {
	raw := rule("rule", "separate", "must", a, b)
	for _, bad := range [][]byte{append(append([]byte(nil), raw...), 0), append(append([]byte(nil), raw...), raw...), raw[:len(raw)-1], nil} {
		if _, err := Decode(bad); err == nil {
			t.Fatal("nonexact original framing accepted")
		}
		encoded, _ := json.Marshal(wire{Version: 1, SourceContract: Source, Originals: map[string][]byte{"rule": bad}})
		var restored State
		if json.Unmarshal(encoded, &restored) == nil {
			t.Fatal("state restoration accepted nonexact original framing")
		}
	}
}
