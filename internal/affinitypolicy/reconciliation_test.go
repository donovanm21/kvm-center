package affinitypolicy

import (
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"strings"
	"testing"
)

func TestReconciliationTransitions(t *testing.T) {
	old := fixture()
	q := Reservation{RequestID: "first", UUID: a, Name: "one", Source: "source", Destination: "target", DestinationAddress: "192.0.2.3"}
	prepared := old.Clone()
	prepared.Version = 2
	prepared.Reservation = &q
	prepared.ReservationPhase = "prepared"
	dispatched := prepared.Clone()
	dispatched.ReservationPhase = "dispatch_possible"
	aborted := prepared.Clone()
	aborted.Reservation = nil
	aborted.ReservationPhase = ""
	aborted.Aborted = map[string]Reservation{q.RequestID: q}
	next := aborted.Clone()
	q2 := q
	q2.RequestID = "second"
	next.Reservation = &q2
	next.ReservationPhase = "prepared"
	for _, pair := range [][2]*State{{old, prepared}, {prepared, dispatched}, {prepared, aborted}, {aborted, next}, {dispatched, dispatched}} {
		if err := ValidateTransition(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	legacy := old.Clone()
	legacy.Reservation = &q
	for _, pair := range [][2]*State{{old, dispatched}, {dispatched, aborted}, {dispatched, prepared}, {aborted, old}, {aborted, prepared}, {legacy, prepared}, {legacy, old}, {prepared, old}} {
		if ValidateTransition(pair[0], pair[1]) == nil {
			t.Fatal("unsafe transition allowed")
		}
	}
	raw, err := json.Marshal(aborted)
	if err != nil {
		t.Fatal(err)
	}
	var got State
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, err = got.Check([]Placement{{a, "one", "source"}, {b, "two", "peer"}}, q); err == nil {
		t.Fatal("aborted replay allowed")
	}
	for _, bad := range []string{strings.Replace(string(raw), `"request_id"`, `"Request_ID"`, 1), strings.Replace(string(raw), `"version":2`, `"version":1`, 1), strings.Replace(string(raw), `"aborted":{`, `"aborted":null,"aborted":{`, 1)} {
		if json.Unmarshal([]byte(bad), &got) == nil {
			t.Fatal("invalid reconciliation JSON accepted")
		}
	}
}

func TestReconciliationEncodedBoundRoundTrips(t *testing.T) {
	s := fixture()
	s.Version = 2
	s.Aborted = map[string]Reservation{}
	var original bson.D
	if err := bson.Unmarshal(s.Originals["separate"], &original); err != nil {
		t.Fatal(err)
	}
	original = append(original, bson.E{Key: "createdat", Value: strings.Repeat("x", 500000)})
	s.Originals["separate"], _ = bson.Marshal(original)
	escaped := "x" + strings.Repeat("\x01", 250) + "x"
	var last []byte
	for i := 0; i < 256; i++ {
		id := fmt.Sprintf("%03d", i) + escaped
		q := Reservation{RequestID: id, UUID: a, Name: escaped, Source: escaped, Destination: "z" + escaped, DestinationAddress: "192.0.2.3"}
		s.Aborted[id] = q
		if (i+1)%32 != 0 {
			continue
		}
		raw, err := json.Marshal(s)
		if err != nil {
			if len(last) == 0 {
				t.Fatal("no accepted boundary")
			}
			var restored State
			if err = json.Unmarshal(last, &restored); err != nil {
				t.Fatal("accepted state cannot reload", err)
			}
			return
		}
		last = raw
	}
	t.Fatal("oversized encoded ledger was accepted")
}
