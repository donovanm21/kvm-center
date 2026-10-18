package offlinestaging

import (
	"bytes"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"strings"
	"testing"
)

func TestLegacyHistoryStateCodec(t *testing.T) {
	s := &LegacyHistoryState{Version: 1, SourceContract: LegacyHistorySource, Records: map[string][]byte{"synthetic-host": raw(t, legacyRecord(legacyPoint()))}}
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back LegacyHistoryState
	if err = json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Digest()
	b, _ := back.Digest()
	if a != b || !bytes.Equal(s.Records["synthetic-host"], back.Records["synthetic-host"]) {
		t.Fatal("raw roundtrip")
	}
	clone := s.Clone()
	clone.Records["synthetic-host"][0] ^= 1
	if bytes.Equal(clone.Records["synthetic-host"], s.Records["synthetic-host"]) {
		t.Fatal("clone aliases original")
	}
	for _, bad := range []string{strings.Replace(string(encoded), `"version":1`, `"version":2`, 1), strings.Replace(string(encoded), `"version":1`, `"Version":1`, 1), strings.Replace(string(encoded), `"version":1`, `"version":1,"version":1`, 1), strings.Replace(string(encoded), `"records":`, `"extra":true,"records":`, 1), `{"version":1,"source_contract":"` + LegacyHistorySource + `","records":{}}`, `null`} {
		if json.Unmarshal([]byte(bad), &back) == nil {
			t.Fatal("accepted malformed contract")
		}
	}
	changed := s.Clone()
	changed.Records["wrong"] = changed.Records["synthetic-host"]
	delete(changed.Records, "synthetic-host")
	if changed.Validate() == nil {
		t.Fatal("identity mismatch accepted")
	}
	changed = s.Clone()
	changed.Records["synthetic-host"] = make([]byte, LegacyHistoryMaxBytes+1)
	if changed.Validate() == nil {
		t.Fatal("size bound ignored")
	}
}

func TestLegacyHistoryStateMixedCaseIdentity(t *testing.T) {
	doc := legacyRecord(legacyPoint())
	doc = setLegacyField(doc, "_id", "HostA")
	doc = setLegacyField(doc, "host", "HostA")
	raw, err := bson.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	state := LegacyHistoryState{Version: 1, SourceContract: LegacyHistorySource, Records: map[string][]byte{"HostA": raw}}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var back LegacyHistoryState
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Records["HostA"], raw) {
		t.Fatal("mixed-case identity changed")
	}
}
