package growprotocol

import (
	"strings"
	"testing"
)

func TestEnvelopeRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{`{"grow_request_v1":null}`, `{"Grow_Request_V1":{}}`, `{"grow_request_v1":{},"grow_request_v1":{}}`, `{"status":"ok","status":"ok"}`, `{"status":"ok"}{}`} {
		if Envelope([]byte(raw), "grow_request_v1") == nil {
			t.Fatal("ambiguous envelope accepted", raw)
		}
	}
	if Envelope([]byte(`{"status":"ok","unrelated_legacy_field":true}`), "grow_request_v1") != nil {
		t.Fatal("legacy envelope refused")
	}
	r := Request{1, "r", strings.Repeat("a", 64), Target{"11111111-1111-4111-8111-111111111111", "serial", "22222222-2222-4222-8222-222222222222", "ext4", "whole-disk"}, 8192}
	s := &State{1, []Record{{r, "offered", nil}}}
	n := s.Clone()
	o := Result(r, "refused_native_unqualified", 0)
	n.Records[0].Phase = "reported"
	n.Records[0].Outcome = &o
	if Transition(s, n) != nil || Transition(n, s) == nil {
		t.Fatal("outcome not monotonic")
	}
	o.NativeVerified = true
	if o.Validate() == nil {
		t.Fatal("native claim accepted")
	}
}
