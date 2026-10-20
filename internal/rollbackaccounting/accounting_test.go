package rollbackaccounting

import (
	"bytes"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"strings"
	"testing"
)

func doc(id string, fields ...bson.E) []byte {
	raw, e := bson.Marshal(append(bson.D{{Key: "_id", Value: id}}, fields...))
	if e != nil {
		panic(e)
	}
	return raw
}
func input(docs ...[]byte) Input {
	return Input{Schema: Schema, SourceRevision: strings.Repeat("a", 40), ArtifactSHA256: strings.Repeat("b", 64), OriginalHashes: map[string]string{"original": strings.Repeat("c", 64)}, Collections: map[string][][]byte{"vms": docs}, Sections: map[string]json.RawMessage{"state": json.RawMessage(`{"v":1}`)}, Indexes: map[string][]byte{"vms": []byte("inert index bytes")}}
}
func TestAccountEveryRecordAndType(t *testing.T) {
	a := input(doc("unchanged", bson.E{Key: "opaque", Value: int32(9)}), doc("edited", bson.E{Key: "value", Value: int32(1)}, bson.E{Key: "deleted", Value: true}), doc("deleted"))
	b := input(doc("added"), doc("edited", bson.E{Key: "value", Value: int64(1)}, bson.E{Key: "added", Value: false}), a.Collections["vms"][0])
	b.Collections["empty"] = [][]byte{}
	b.Sections["state"] = json.RawMessage(`{"v":2}`)
	b.Indexes["vms"] = []byte("changed inert index bytes")
	r, e := Compare(a, b)
	if e != nil {
		t.Fatal(e)
	}
	c := r.Collections[1]
	if c.BeforeCount != 3 || c.AfterCount != 3 || c.Unchanged != 1 || c.Added != 1 || c.Deleted != 1 || c.Changed != 1 {
		t.Fatal(c)
	}
	if r.Collections[0].BeforePresent || !r.Collections[0].AfterPresent || len(r.Sections) != 1 || len(r.Indexes) != 1 || r.Activation || r.ReplaySupported || r.RollbackSafe {
		t.Fatal(r)
	}
	for _, x := range c.Records {
		if x.IdentitySHA256 == Hash([]byte("edited")) {
			if len(x.Fields) != 3 {
				t.Fatal(x)
			}
			for _, f := range x.Fields {
				if f.KeySHA256 == Hash([]byte("value")) && (f.Before.Type != "32-bit integer" || f.After.Type != "64-bit integer") {
					t.Fatal(f)
				}
			}
		}
	}
	raw, _ := json.Marshal(r)
	for _, secret := range []string{"opaque", "edited", "inert index bytes"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("raw value/identity leaked")
		}
	}
	a.Collections["vms"][0], a.Collections["vms"][2] = a.Collections["vms"][2], a.Collections["vms"][0]
	again, e := Compare(a, b)
	if e != nil {
		t.Fatal(e)
	}
	same, _ := json.Marshal(again)
	if !bytes.Equal(raw, same) {
		t.Fatal("input ordering changed report")
	}
}
func TestRefuseAmbiguousInput(t *testing.T) {
	valid := input(doc("one"))
	cases := []Input{}
	x := input(doc("one"), doc("one"))
	cases = append(cases, x)
	x = input(doc("one", bson.E{Key: "x", Value: 1}, bson.E{Key: "x", Value: 2}))
	cases = append(cases, x)
	x = input(doc("one", bson.E{Key: "nested", Value: bson.D{{Key: "x", Value: 1}, {Key: "x", Value: 2}}}))
	cases = append(cases, x)
	x = input([]byte{5, 0, 0, 0})
	cases = append(cases, x)
	x = input(doc("one"))
	x.Sections["state"] = json.RawMessage(`{"x":1,"x":2}`)
	cases = append(cases, x)
	x = input(doc("one"))
	x.ArtifactSHA256 = strings.Repeat("d", 64)
	cases = append(cases, x)
	x = input(doc("one"))
	x.OriginalHashes["original"] = strings.Repeat("d", 64)
	cases = append(cases, x)
	for i, c := range cases {
		if _, e := Compare(valid, c); e == nil {
			t.Fatalf("accepted ambiguous %d", i)
		}
	}
}

func TestArrayFramingAndOpaqueBytes(t *testing.T) {
	raw := doc("one", bson.E{Key: "items", Value: bson.A{int32(8)}})
	bad := bytes.Replace(raw, []byte{0x10, '0', 0}, []byte{0x10, '1', 0}, 1)
	if bytes.Equal(raw, bad) {
		t.Fatal("array fixture not altered")
	}
	for _, b := range [][]byte{bad, append(append([]byte{}, raw...), 0)} {
		if _, e := Compare(input(raw), input(b)); e == nil {
			t.Fatal("ambiguous BSON accepted")
		}
	}
	after := doc("one", bson.E{Key: "items", Value: bson.A{int32(9)}})
	r, e := Compare(input(raw), input(after))
	if e != nil {
		t.Fatal(e)
	}
	fields := r.Collections[0].Records[0].Fields
	if len(fields) != 1 || fields[0].KeySHA256 != Hash([]byte("items")) || fields[0].Before.Type != "array" || fields[0].After.Type != "array" {
		t.Fatal("array was not accounted atomically")
	}
}

func TestMalformedUTF8RefusedAndRawSectionBinding(t *testing.T) {
	good := input(doc("one"))
	cases := []Input{}
	x := input(doc(string([]byte{0xff})))
	cases = append(cases, x)
	x = input(doc("one", bson.E{Key: string([]byte{0xff}), Value: 1}))
	cases = append(cases, x)
	x = input(doc("one"))
	x.Sections["state"] = json.RawMessage([]byte{'"', 0xff, '"'})
	cases = append(cases, x)
	x = input(doc("one"))
	x.Sections[string([]byte{0xff})] = json.RawMessage(`null`)
	cases = append(cases, x)
	x = input(doc("one"))
	x.Indexes[string([]byte{0xff})] = []byte("x")
	cases = append(cases, x)
	x = input(doc("one"))
	x.OriginalHashes[string([]byte{0xff})] = strings.Repeat("a", 64)
	cases = append(cases, x)
	for i, c := range cases {
		if _, e := Compare(good, c); e == nil {
			t.Fatalf("accepted malformed UTF8 %d", i)
		}
	}
	x = input(doc("one"))
	x.Sections["state"] = json.RawMessage(`{ "v":1 }`)
	r, e := Compare(good, x)
	if e != nil {
		t.Fatal(e)
	}
	if r.BeforeSHA256 == r.AfterSHA256 || len(r.Sections) != 1 {
		t.Fatal("raw section difference omitted from binding")
	}
}
