// Package rollbackaccounting reports exact synthetic snapshot differences. It
// cannot approve an old writer, apply a delta, or authorize activation.
package rollbackaccounting

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
)

const Schema = "synthetic-current-write-accounting-v1"

// Input is an explicitly scoped current-format snapshot. Sections bind the
// complete application snapshot; Collections provide its raw entity overlay.
// OriginalHashes anchor separately retained originals, not a conversion proof.
type Input struct {
	Schema         string                     `json:"schema"`
	SourceRevision string                     `json:"source_revision"`
	ArtifactSHA256 string                     `json:"artifact_sha256"`
	OriginalHashes map[string]string          `json:"original_hashes"`
	Sections       map[string]json.RawMessage `json:"sections"`
	Collections    map[string][][]byte        `json:"collections"`
	Indexes        map[string][]byte          `json:"indexes"`
}
type Digest struct {
	Present bool   `json:"present"`
	SHA256  string `json:"sha256,omitempty"`
	Type    string `json:"type,omitempty"`
}
type Change struct {
	KeySHA256 string `json:"key_sha256"`
	Before    Digest `json:"before"`
	After     Digest `json:"after"`
}
type Record struct {
	IdentitySHA256 string   `json:"identity_sha256"`
	Before         Digest   `json:"before"`
	After          Digest   `json:"after"`
	Fields         []Change `json:"fields"`
}
type Collection struct {
	Name          string   `json:"name"`
	BeforePresent bool     `json:"before_present"`
	AfterPresent  bool     `json:"after_present"`
	BeforeCount   int      `json:"before_count"`
	AfterCount    int      `json:"after_count"`
	Unchanged     int      `json:"unchanged"`
	Added         int      `json:"added"`
	Deleted       int      `json:"deleted"`
	Changed       int      `json:"changed"`
	Records       []Record `json:"records"`
}
type Report struct {
	SourceRevision   string       `json:"source_revision"`
	ArtifactSHA256   string       `json:"artifact_sha256"`
	OriginalsSHA256  string       `json:"originals_sha256"`
	SectionInventory []Change     `json:"section_inventory"`
	IndexInventory   []Change     `json:"index_inventory"`
	Schema           string       `json:"schema"`
	Activation       bool         `json:"activation"`
	ReplaySupported  bool         `json:"replay_supported"`
	RollbackSafe     bool         `json:"rollback_safe"`
	BeforeSHA256     string       `json:"before_sha256"`
	AfterSHA256      string       `json:"after_sha256"`
	Collections      []Collection `json:"collections"`
	Sections         []Change     `json:"sections"`
	Indexes          []Change     `json:"indexes"`
}

func Hash(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }
func digest(raw []byte, present bool, typ string) Digest {
	if !present {
		return Digest{}
	}
	return Digest{true, Hash(raw), typ}
}
func keys[A any, B any](a map[string]A, b map[string]B) []string {
	m := map[string]bool{}
	for k := range a {
		m[k] = true
	}
	for k := range b {
		m[k] = true
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func validHash(s string, n int) bool {
	raw, e := hex.DecodeString(s)
	return e == nil && len(raw) == n && hex.EncodeToString(raw) == s
}

// validateBSON refuses ambiguous keys and array layouts. Nested documents are
// validated, but compared as whole BSON values: no inferred array identities.
func validateBSON(raw bson.Raw, array bool, depth int) error {
	if depth > 64 || len(raw) > 16<<20 || len(raw) < 5 || int(binary.LittleEndian.Uint32(raw[:4])) != len(raw) {
		return errors.New("BSON bounds")
	}
	if e := raw.Validate(); e != nil {
		return errors.New("invalid BSON framing")
	}
	els, e := raw.Elements()
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	for i, el := range els {
		k := el.Key()
		if !utf8.ValidString(k) || seen[k] || (array && k != strconv.Itoa(i)) {
			return errors.New("ambiguous BSON keys")
		}
		seen[k] = true
		v := el.Value()
		switch v.Type {
		case bsontype.CodeWithScope:
			_, scope, ok := v.CodeWithScopeOK()
			if !ok {
				return errors.New("invalid scope")
			}
			if e := validateBSON(scope, false, depth+1); e != nil {
				return e
			}
		case bsontype.EmbeddedDocument, bsontype.Array:
			if e := validateBSON(bson.Raw(v.Value), v.Type == bsontype.Array, depth+1); e != nil {
				return e
			}
		}
	}
	return nil
}
func records(in [][]byte) (map[string][]byte, error) {
	if len(in) > 4096 {
		return nil, errors.New("record bound")
	}
	out := map[string][]byte{}
	for _, raw := range in {
		if e := validateBSON(bson.Raw(raw), false, 0); e != nil {
			return nil, e
		}
		id, ok := bson.Raw(raw).Lookup("_id").StringValueOK()
		if !ok || id == "" || !utf8.ValidString(id) || out[id] != nil {
			return nil, errors.New("ambiguous record identity")
		}
		out[id] = raw
	}
	return out, nil
}
func strictJSON(raw []byte) error {
	if !utf8.Valid(raw) || len(raw) > 32<<20 {
		return errors.New("JSON bound")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON depth")
		}
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return errors.New("ambiguous JSON keys")
				}
				seen[key] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := walk(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func validate(in Input) error {
	if in.Schema != Schema || !validHash(in.SourceRevision, 20) || !validHash(in.ArtifactSHA256, 32) || len(in.OriginalHashes) == 0 || len(in.Collections) > 64 || len(in.Sections) > 128 || len(in.Indexes) > 64 {
		return errors.New("unsupported snapshot binding")
	}
	for key := range in.OriginalHashes {
		if !utf8.ValidString(key) || key == "" {
			return errors.New("invalid original key")
		}
	}
	for key := range in.Sections {
		if !utf8.ValidString(key) || key == "" {
			return errors.New("invalid section key")
		}
	}
	for key := range in.Indexes {
		if !utf8.ValidString(key) || key == "" {
			return errors.New("invalid index key")
		}
	}
	for _, h := range in.OriginalHashes {
		if !validHash(h, 32) {
			return errors.New("invalid original hash")
		}
	}
	for _, raw := range in.Sections {
		if e := strictJSON(raw); e != nil {
			return e
		}
	}
	for name := range in.Collections {
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`).MatchString(name) {
			return errors.New("invalid collection name")
		}
	}
	for _, docs := range in.Collections {
		if _, e := records(docs); e != nil {
			return e
		}
	}
	return nil
}
func fieldMap(raw []byte) map[string]bson.RawValue {
	m := map[string]bson.RawValue{}
	if raw != nil {
		els, _ := bson.Raw(raw).Elements()
		for _, e := range els {
			m[e.Key()] = e.Value()
		}
	}
	return m
}
func fields(a, b []byte) []Change {
	x, y := fieldMap(a), fieldMap(b)
	out := []Change{}
	for _, key := range keys(x, y) {
		v, ok := x[key]
		w, yes := y[key]
		if ok == yes && v.Type == w.Type && bytes.Equal(v.Value, w.Value) {
			continue
		}
		out = append(out, Change{Hash([]byte(key)), digest(v.Value, ok, v.Type.String()), digest(w.Value, yes, w.Type.String())})
	}
	return out
}
func changes(a, b map[string][]byte) []Change {
	out := []Change{}
	for _, k := range keys(a, b) {
		v, ok := a[k]
		w, yes := b[k]
		if ok != yes || !bytes.Equal(v, w) {
			out = append(out, Change{Hash([]byte(k)), digest(v, ok, ""), digest(w, yes, "")})
		}
	}
	return out
}
func inventory(a, b map[string][]byte) []Change {
	out := []Change{}
	for _, k := range keys(a, b) {
		v, ok := a[k]
		w, yes := b[k]
		out = append(out, Change{Hash([]byte(k)), digest(v, ok, ""), digest(w, yes, "")})
	}
	return out
}
func sections(in map[string]json.RawMessage) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func canonical(in Input) Input {
	out := in
	out.Collections = map[string][][]byte{}
	for name, docs := range in.Collections {
		copyDocs := append([][]byte{}, docs...)
		sort.Slice(copyDocs, func(i, j int) bool {
			return bson.Raw(copyDocs[i]).Lookup("_id").StringValue() < bson.Raw(copyDocs[j]).Lookup("_id").StringValue()
		})
		out.Collections[name] = copyDocs
	}
	return out
}

func inputDigest(in Input) string {
	in = canonical(in)
	// RawMessage marshaling compacts whitespace. Bind raw section bytes instead,
	// so every reported byte difference also changes the complete input digest.
	raw, _ := json.Marshal(map[string]any{"schema": in.Schema, "source_revision": in.SourceRevision, "artifact_sha256": in.ArtifactSHA256, "original_hashes": in.OriginalHashes, "sections": sections(in.Sections), "collections": in.Collections, "indexes": in.Indexes})
	return Hash(raw)
}

// Compare binds both complete input inventories and reports every raw entity
// document. Field names and identities are hashed; no values are published.
// Unchanged records still carry their digest. Missing and empty differ.
func Compare(before, after Input) (*Report, error) {
	if e := validate(before); e != nil {
		return nil, e
	}
	if e := validate(after); e != nil {
		return nil, e
	}
	if before.SourceRevision != after.SourceRevision || before.ArtifactSHA256 != after.ArtifactSHA256 {
		return nil, errors.New("different current artifacts")
	}
	x, _ := json.Marshal(before.OriginalHashes)
	y, _ := json.Marshal(after.OriginalHashes)
	if !bytes.Equal(x, y) {
		return nil, errors.New("original binding changed")
	}

	originals, _ := json.Marshal(before.OriginalHashes)
	r := &Report{SourceRevision: before.SourceRevision, ArtifactSHA256: before.ArtifactSHA256, OriginalsSHA256: Hash(originals), SectionInventory: inventory(sections(before.Sections), sections(after.Sections)), IndexInventory: inventory(before.Indexes, after.Indexes), Schema: Schema, BeforeSHA256: inputDigest(before), AfterSHA256: inputDigest(after), Collections: []Collection{}, Sections: changes(sections(before.Sections), sections(after.Sections)), Indexes: changes(before.Indexes, after.Indexes)}
	for _, name := range keys(before.Collections, after.Collections) {
		a, ap := before.Collections[name]
		b, bp := after.Collections[name]
		am, _ := records(a)
		bm, _ := records(b)
		c := Collection{Name: name, BeforePresent: ap, AfterPresent: bp, BeforeCount: len(am), AfterCount: len(bm), Records: []Record{}}
		for _, id := range keys(am, bm) {
			v, ok := am[id]
			w, yes := bm[id]
			switch {
			case !ok:
				c.Added++
			case !yes:
				c.Deleted++
			case bytes.Equal(v, w):
				c.Unchanged++
			default:
				c.Changed++
			}
			c.Records = append(c.Records, Record{Hash([]byte(id)), digest(v, ok, "document"), digest(w, yes, "document"), fields(v, w)})
		}
		if c.BeforeCount != c.Unchanged+c.Changed+c.Deleted || c.AfterCount != c.Unchanged+c.Changed+c.Added {
			return nil, fmt.Errorf("accounting mismatch")
		}
		r.Collections = append(r.Collections, c)
	}
	return r, nil
}
