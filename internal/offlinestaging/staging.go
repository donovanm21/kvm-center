package offlinestaging

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

const Contract = "kvm-offline-bson-v1"
const maxSnapshot = 128 << 20

var safeName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Input describes an already-created snapshot. Claims are recorded, not trusted
// as proof of snapshot coherence or recovery completeness. All files are flat.
type Input struct {
	Contract             string       `json:"contract"`
	SourceSchema         string       `json:"source_schema"`
	SourceBuild          string       `json:"source_build"`
	TargetArtifactSHA256 string       `json:"target_artifact_sha256"`
	VerificationClock    string       `json:"verification_clock"`
	ConsistencyEvidence  string       `json:"consistency_evidence"`
	CompanionEvidence    string       `json:"companion_evidence"`
	Collections          []Collection `json:"collections"`
}
type Collection struct {
	Name        string `json:"name"`
	Documents   int    `json:"documents"`
	DataSHA256  string `json:"data_sha256"`
	IndexSHA256 string `json:"index_sha256"`
}
type Entry struct {
	Collection       string   `json:"collection"`
	Ordinal          int      `json:"ordinal"`
	OriginalSHA256   string   `json:"original_sha256"`
	NormalizedSHA256 string   `json:"normalized_sha256"`
	Changes          []string `json:"changes"`
}
type Manifest struct {
	Contract     string            `json:"contract"`
	Status       string            `json:"status"`
	Activation   bool              `json:"activation"`
	SourceSHA256 string            `json:"source_sha256"`
	Files        map[string]string `json:"files"`
	Blockers     []string          `json:"blockers"`
}

var blockers = []string{
	"Application activation forbidden: full-schema runtime round-trip is unproven.",
	"Legacy history remains raw: no units, aggregates or bounded-store conversion inferred.",
	"Snapshot coherence, build lineage and companion recovery claims are not independently proven.",
	"Index metadata only: no database, TTL, telemetry, jobs or background writers are started.",
}

type snapshot struct {
	manifest   Input
	source     []byte
	files      map[string][]byte
	normalized map[string][]byte
	ledger     []Entry
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// reject duplicate JSON keys before decoding into structs (which otherwise use
// last-key-wins). Bound input before tokenization; reject unknown struct fields.
func decode(data []byte, target any) error {
	if len(data) > 4<<20 {
		return errors.New("JSON too large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errors.New("JSON too deep")
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
				t, e = d.Token()
				if e != nil {
					return e
				}
				k, ok := t.(string)
				if !ok || k != strings.ToLower(k) || seen[k] {
					return errors.New("duplicate JSON key")
				}
				seen[k] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			t, e = d.Token()
			if e != nil || t != json.Delim('}') {
				return errors.New("invalid JSON object")
			}
		case '[':
			for d.More() {
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			t, e = d.Token()
			if e != nil || t != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		return nil
	}
	if err := value(0); err != nil {
		return errors.New("invalid/ambiguous JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("invalid manifest fields")
	}
	return nil
}

func openRoot(path string) (*os.Root, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	if real != abs {
		return nil, errors.New("symlink directory refused")
	}
	return os.OpenRoot(abs)
}
func singleLink(info os.FileInfo) bool {
	// FileInfo.Sys differs across platforms; fail closed on platforms without a
	// verifiable link count instead of silently accepting hard-linked snapshots.
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return false
	}
	n := v.FieldByName("Nlink")
	return n.IsValid() && n.CanUint() && n.Uint() == 1
}
func readFile(root *os.Root, name string, limit int) ([]byte, error) {
	st, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || !singleLink(st) || st.Size() > int64(limit) {
		return nil, errors.New("linked/nonregular/oversized input refused")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(st, after) {
		return nil, errors.New("input changed during open")
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(b) > limit {
		return nil, errors.New("input read/size failure")
	}
	return b, nil
}
func inventory(root *os.Root, want map[string][]byte) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	if len(entries) != len(want) {
		return errors.New("undeclared or missing files")
	}
	for _, e := range entries {
		if _, ok := want[e.Name()]; !ok {
			return errors.New("undeclared file")
		}
	}
	return nil
}
func parseSnapshot(source []byte, files map[string][]byte) (*snapshot, error) {
	var m Input
	if err := decode(source, &m); err != nil {
		return nil, err
	}
	if m.Contract != Contract || m.SourceSchema != "synthetic-full-bson-v1" || m.SourceBuild == "" || !hashPattern.MatchString(m.TargetArtifactSHA256) || m.VerificationClock == "" || m.ConsistencyEvidence == "" || m.CompanionEvidence == "" {
		return nil, errors.New("unsupported/incomplete source contract (synthetic snapshots only)")
	}
	if _, err := time.Parse(time.RFC3339Nano, m.VerificationClock); err != nil || !strings.HasSuffix(m.VerificationClock, "Z") {
		return nil, errors.New("invalid UTC verification clock")
	}
	if len(m.Collections) == 0 || len(m.Collections) > 128 {
		return nil, errors.New("collection count refused")
	}
	s := &snapshot{manifest: m, source: source, files: files, normalized: map[string][]byte{}, ledger: []Entry{}}
	all := map[string][]bson.Raw{}
	previous := ""
	for _, c := range m.Collections {
		if !safeName.MatchString(c.Name) || c.Name <= previous || c.Documents < 0 || c.Documents > 100000 || !hashPattern.MatchString(c.DataSHA256) || !hashPattern.MatchString(c.IndexSHA256) {
			return nil, errors.New("invalid/unsorted collection declaration")
		}
		previous = c.Name
		data, ok := files[c.Name+".bson"]
		if !ok || digest(data) != c.DataSHA256 {
			return nil, errors.New("source BSON hash mismatch")
		}
		idx, ok := files[c.Name+".indexes.bson"]
		if !ok || digest(idx) != c.IndexSHA256 {
			return nil, errors.New("index metadata hash mismatch")
		}
		docs, err := documents(data)
		if err != nil {
			return nil, err
		}
		if len(docs) != c.Documents {
			return nil, errors.New("document count mismatch")
		}
		normalized := []byte{}
		all[c.Name] = []bson.Raw{}
		for i, d := range docs {
			n, changes, err := normalize(c.Name, d)
			if err != nil {
				return nil, err
			}
			if err = verifyElements(c.Name, d, n); err != nil {
				return nil, err
			}
			normalized = append(normalized, n...)
			all[c.Name] = append(all[c.Name], n)
			s.ledger = append(s.ledger, Entry{c.Name, i, digest(d), digest(n), changes})
		}
		if err := validateIndexes(c.Name, idx, all[c.Name]); err != nil {
			return nil, err
		}
		s.normalized[c.Name] = normalized
	}
	if len(files) != len(m.Collections)*2 {
		return nil, errors.New("undeclared snapshot data")
	}
	if err := validateDocuments(all); err != nil {
		return nil, err
	}
	return s, nil
}
func loadInput(root *os.Root) (*snapshot, error) {
	source, err := readFile(root, "snapshot.json", 4<<20)
	if err != nil {
		return nil, err
	}
	var m Input
	if err = decode(source, &m); err != nil {
		return nil, err
	}
	if len(m.Collections) > 128 {
		return nil, errors.New("too many collections")
	}
	files := map[string][]byte{}
	want := map[string][]byte{"snapshot.json": source}
	total := len(source)
	for _, c := range m.Collections {
		if !safeName.MatchString(c.Name) {
			return nil, errors.New("unsafe collection name")
		}
		for _, suffix := range []string{".bson", ".indexes.bson"} {
			name := c.Name + suffix
			b, err := readFile(root, name, maxSnapshot-total)
			if err != nil {
				return nil, err
			}
			total += len(b)
			if total > maxSnapshot {
				return nil, errors.New("snapshot size refused")
			}
			files[name] = b
			want[name] = b
		}
	}
	if err = inventory(root, want); err != nil {
		return nil, err
	}
	return parseSnapshot(source, files)
}
func bundle(s *snapshot) (map[string][]byte, error) {
	files := map[string][]byte{"source.json": s.source}
	for _, c := range s.manifest.Collections {
		files["original-"+c.Name+".bson"] = s.files[c.Name+".bson"]
		files["indexes-"+c.Name+".bson"] = s.files[c.Name+".indexes.bson"]
		files["normalized-"+c.Name+".bson"] = s.normalized[c.Name]
	}
	ledger, err := json.MarshalIndent(s.ledger, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(ledger) > 4<<20 {
		return nil, errors.New("ledger too large")
	}
	files["ledger.json"] = ledger
	return files, nil
}
func manifest(s *snapshot, files map[string][]byte) Manifest {
	m := Manifest{Contract, "VERIFIED_OFFLINE", false, digest(s.source), map[string]string{}, append([]string(nil), blockers...)}
	for n, b := range files {
		m.Files[n] = digest(b)
	}
	return m
}

// Convert accepts only the declared synthetic contract. It has no DB endpoint,
// credentials, activation, delete, resume, expiry or pruning option.
func Convert(source, output string) (*Manifest, error) { return convert(source, output, nil) }
func convert(source, output string, hook func(string) error) (*Manifest, error) {
	src, err := openRoot(source)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	s, err := loadInput(src)
	if err != nil {
		return nil, err
	}
	files, err := bundle(s)
	if err != nil {
		return nil, err
	}
	expected := manifest(s, files)
	abs, err := filepath.Abs(output)
	if err != nil {
		return nil, err
	}
	srcAbs, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	// Compare ancestors correctly, including dot-prefixed directories.
	for p := abs; p != filepath.Dir(p); p = filepath.Dir(p) {
		if p == srcAbs {
			return nil, errors.New("output inside source")
		}
	}
	for p := srcAbs; p != filepath.Dir(p); p = filepath.Dir(p) {
		if p == abs {
			return nil, errors.New("source inside output")
		}
	}
	parent, err := openRoot(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if err = parent.Mkdir(filepath.Base(abs), 0700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		got, err := Verify(abs)
		if err != nil {
			return nil, err
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(expected)
		if !bytes.Equal(a, b) {
			return nil, errors.New("existing output differs")
		}
		if hook != nil {
			if e := hook("noop:before-source-recheck"); e != nil {
				return nil, e
			}
		}
		again, reloadErr := loadInput(src)
		if reloadErr != nil {
			return nil, reloadErr
		}
		if !sameSnapshot(s, again) {
			return nil, errors.New("source changed during no-op")
		}
		return got, nil
	}
	out, err := openRoot(abs)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	step := func(label string) error {
		if hook != nil {
			return hook(label)
		}
		return nil
	}
	syncDir := func(root *os.Root, label string) error {
		f, e := root.Open(".")
		if e != nil {
			return e
		}
		e = f.Sync()
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		return step(label)
	}
	write := func(name string, data []byte) error {
		f, e := out.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		if e = step("create:" + name); e != nil {
			f.Close()
			return e
		}
		_, e = f.Write(data)
		if e == nil {
			e = step("write:" + name)
		}
		if e == nil {
			e = f.Sync()
		}
		if e == nil {
			e = step("sync:" + name)
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	}
	// No final manifest means NOT_READY. Partial output is never resumed/overwritten.
	if err = syncDir(parent, "sync:parent-created"); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err = write(n, files[n]); err != nil {
			return nil, err
		}
	}
	again, err := loadInput(src)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(s.source, again.source) {
		return nil, errors.New("source manifest changed")
	}
	for n, b := range s.files {
		if !bytes.Equal(b, again.files[n]) {
			return nil, errors.New("source changed")
		}
	}
	// Read back each durable output before committing readiness.
	for n, b := range files {
		actual, e := readFile(out, n, maxSnapshot)
		if e != nil || !bytes.Equal(b, actual) {
			return nil, errors.New("output verification failed")
		}
	}
	encoded, _ := json.MarshalIndent(expected, "", "  ")
	if err = write("manifest.pending", encoded); err != nil {
		return nil, err
	}
	if err = syncDir(out, "sync:before-commit"); err != nil {
		return nil, err
	}
	if err = out.Rename("manifest.pending", "manifest.json"); err != nil {
		return nil, err
	}
	if err = step("rename:manifest"); err != nil {
		return nil, err
	}
	if err = syncDir(out, "sync:committed"); err != nil {
		return nil, err
	}
	return Verify(abs)
}

// Verify revalidates every raw record, transformation, index and file inventory.
// A digest alone does not authenticate who created a snapshot.
func Verify(path string) (*Manifest, error) {
	m, _, err := verifySnapshot(path)
	return m, err
}
func verifySnapshot(path string) (*Manifest, *snapshot, error) {
	root, err := openRoot(path)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	raw, err := readFile(root, "manifest.json", 4<<20)
	if err != nil {
		return nil, nil, errors.New("NOT_READY: no readable committed manifest")
	}
	var m Manifest
	if err = decode(raw, &m); err != nil {
		return nil, nil, err
	}
	if m.Contract != Contract || m.Status != "VERIFIED_OFFLINE" || m.Activation || len(m.Files) > 386 {
		return nil, nil, errors.New("invalid offline manifest")
	}
	source, err := readFile(root, "source.json", 4<<20)
	if err != nil {
		return nil, nil, err
	}
	var input Input
	if err = decode(source, &input); err != nil {
		return nil, nil, err
	}
	if len(input.Collections) > 128 {
		return nil, nil, errors.New("too many collections")
	}
	original := map[string][]byte{}
	total := len(source)
	for _, c := range input.Collections {
		if !safeName.MatchString(c.Name) {
			return nil, nil, errors.New("unsafe collection")
		}
		for _, pair := range [][2]string{{"original-", ".bson"}, {"indexes-", ".indexes.bson"}} {
			b, e := readFile(root, pair[0]+c.Name+".bson", maxSnapshot-total)
			if e != nil {
				return nil, nil, e
			}
			total += len(b)
			original[c.Name+pair[1]] = b
		}
	}
	s, err := parseSnapshot(source, original)
	if err != nil {
		return nil, nil, err
	}
	files, err := bundle(s)
	if err != nil {
		return nil, nil, err
	}
	expected := manifest(s, files)
	a, _ := json.Marshal(m)
	b, _ := json.Marshal(expected)
	if !bytes.Equal(a, b) {
		return nil, nil, errors.New("manifest/accounting mismatch")
	}
	for n, want := range files {
		got, e := readFile(root, n, maxSnapshot)
		if e != nil || !bytes.Equal(want, got) {
			return nil, nil, errors.New("bundle content mismatch")
		}
	}
	files["manifest.json"] = raw
	if err = inventory(root, files); err != nil {
		return nil, nil, err
	}
	return &m, s, nil
}

// HistoryRecord exposes original BSON without converting its semantics. Callers
// decide where to write raw output; the CLI emits only metadata unless asked.
type HistoryRecord struct {
	Collection string   `json:"collection"`
	Ordinal    int      `json:"ordinal"`
	SHA256     string   `json:"sha256"`
	Raw        bson.Raw `json:"-"`
}

func History(path string) ([]HistoryRecord, error) {
	_, s, err := verifySnapshot(path)
	if err != nil {
		return nil, err
	}
	var result []HistoryRecord
	for _, c := range s.manifest.Collections {
		if c.Name != "perf_history" && c.Name != "host_history_state" {
			continue
		}
		docs, err := documents(s.files[c.Name+".bson"])
		if err != nil {
			return nil, err
		}
		for i, d := range docs {
			result = append(result, HistoryRecord{c.Name, i, digest(d), append(bson.Raw(nil), d...)})
		}
	}
	return result, nil
}

func sameSnapshot(a, b *snapshot) bool {
	if !bytes.Equal(a.source, b.source) || len(a.files) != len(b.files) {
		return false
	}
	for n, v := range a.files {
		if !bytes.Equal(v, b.files[n]) {
			return false
		}
	}
	return true
}

// Summary is deliberately payload-free for logs and public synthetic CI.
func Summary(m *Manifest) string {
	return fmt.Sprintf("%s activation=%t files=%d source_sha256=%s", m.Status, m.Activation, len(m.Files), m.SourceSHA256)
}
