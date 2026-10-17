package offlinestaging

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func raw(t *testing.T, d bson.D) []byte {
	t.Helper()
	b, e := bson.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func write(t *testing.T, p string, b []byte) {
	t.Helper()
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T) (string, map[string][]byte, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	real, e := filepath.EvalSymlinks(dir)
	if e != nil {
		t.Fatal(e)
	}
	dir = real
	data := map[string][]byte{}
	idx := map[string][]byte{}
	// Every current persisted collection plus representative unsupported legacy
	// alarms/affinity, arbitrary future data, and both history representations.
	names := strings.Fields("hosts vms storage vm_notes guest_data migrations command_log users groups roles sessions idps features server_meta audit_events snapshot_policies notifications metrics audit_shipping backup_schedule discord_gateway removed_hosts pending_enrollments api_tokens hypervisor_certs vm_tag_state host_history_state perf_history vm_tags alarms affinity future_collection")
	for _, name := range names {
		id := name + "-1"
		if name == "hosts" {
			id = "host-1"
		}
		d := bson.D{{Key: "_id", Value: id}, {Key: "opaque", Value: bson.D{{Key: "nested", Value: bson.A{int32(3), int64(4), nil}}, {Key: "binary", Value: primitive.Binary{Subtype: 0x80, Data: []byte{0, 1, 255}}}, {Key: "old_date", Value: primitive.DateTime(-1)}, {Key: "decimal", Value: primitive.NewDecimal128(7, 9)}}}}
		switch name {
		case "vms":
			d = append(d, bson.E{Key: "hostid", Value: "host-1"}, bson.E{Key: "uuid", Value: "vm-1"})
		case "sessions":
			d = append(d, bson.E{Key: "expiresat", Value: primitive.DateTime(1)}) // expired is preserved, never extended
		case "migrations":
			d = append(d, bson.E{Key: "startedat", Value: int64(1)})
		case "audit_events":
			d = append(d, bson.E{Key: "actorid", Value: "deleted-actor"})
		case "perf_history":
			d = append(d, bson.E{Key: "points", Value: bson.A{bson.D{{Key: "time", Value: int64(-1)}, {Key: "cpu", Value: 1.25}, {Key: "units", Value: "unknown"}}}})
		case "host_history_state":
			d = append(d, bson.E{Key: "version", Value: int32(1)})
		}
		data[name] = raw(t, d)
		idx[name] = raw(t, bson.D{{Key: "v", Value: int32(2)}, {Key: "name", Value: "_id_"}, {Key: "key", Value: bson.D{{Key: "_id", Value: int32(1)}}}})
	}
	idx["sessions"] = append(idx["sessions"], raw(t, bson.D{{Key: "name", Value: "expires_at_1"}, {Key: "key", Value: bson.D{{Key: "expires_at", Value: int32(1)}}}, {Key: "expireAfterSeconds", Value: int32(0)}})...)
	saveFixture(t, dir, data, idx)
	return dir, data, idx
}
func saveFixture(t *testing.T, dir string, data, idx map[string][]byte) {
	t.Helper()
	m := Input{Contract: Contract, SourceSchema: "synthetic-full-bson-v1", SourceBuild: "synthetic-only", TargetArtifactSHA256: strings.Repeat("a", 64), VerificationClock: "2026-01-01T00:00:00Z", ConsistencyEvidence: "synthetic coherent generator", CompanionEvidence: "synthetic; no real companion files"}
	names := []string{}
	for n := range data {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		docs, e := documents(data[n])
		count := len(docs)
		if e != nil {
			count = 1
		}
		m.Collections = append(m.Collections, Collection{n, count, digest(data[n]), digest(idx[n])})
		write(t, filepath.Join(dir, n+".bson"), data[n])
		write(t, filepath.Join(dir, n+".indexes.bson"), idx[n])
	}
	b, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(dir, "snapshot.json"), b)
}
func freshOutput(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return filepath.Join(p, "stage")
}
func assertSource(t *testing.T, src string, data, idx map[string][]byte) {
	t.Helper()
	for n, b := range data {
		got, e := os.ReadFile(filepath.Join(src, n+".bson"))
		if e != nil || !bytes.Equal(got, b) {
			t.Fatalf("source changed %s", n)
		}
		got, e = os.ReadFile(filepath.Join(src, n+".indexes.bson"))
		if e != nil || !bytes.Equal(got, idx[n]) {
			t.Fatalf("source index changed %s", n)
		}
	}
}

func TestFullSnapshotPreservationRestoreAndRollback(t *testing.T) {
	src, data, idx := fixture(t)
	out := freshOutput(t)
	m, e := Convert(src, out)
	if e != nil {
		t.Fatal(e)
	}
	if m.Activation || m.Status != "VERIFIED_OFFLINE" {
		t.Fatal(m)
	}
	assertSource(t, src, data, idx)
	for n, b := range data {
		got, e := os.ReadFile(filepath.Join(out, "original-"+n+".bson"))
		if e != nil || !bytes.Equal(got, b) {
			t.Fatalf("original %s", n)
		}
		normal, _ := os.ReadFile(filepath.Join(out, "normalized-"+n+".bson"))
		ds, _ := documents(b)
		nd, _ := documents(normal)
		if len(ds) != len(nd) {
			t.Fatal("count")
		}
		for i := range ds {
			if e := verifyElements(n, ds[i], nd[i]); e != nil {
				t.Fatal(e)
			}
		}
	}
	// Reopen/verify uses only disk, no in-memory conversion state. Verified no-op.
	if _, e = Verify(out); e != nil {
		t.Fatal(e)
	}
	if _, e = Convert(src, out); e != nil {
		t.Fatal(e)
	}
	history, e := History(out)
	if e != nil || len(history) != 2 {
		t.Fatalf("history %v %d", e, len(history))
	}
	for _, r := range history {
		if !bytes.Equal(r.Raw, data[r.Collection]) || r.SHA256 != digest(r.Raw) {
			t.Fatal("history reinterpreted")
		}
	}
	// Offline restore to a distinct directory from immutable originals, then
	// reconvert. This is file recovery, not a claim of old-server compatibility.
	restored := t.TempDir()
	restored, _ = filepath.EvalSymlinks(restored)
	source, _ := os.ReadFile(filepath.Join(out, "source.json"))
	write(t, filepath.Join(restored, "snapshot.json"), source)
	for n := range data {
		b, _ := os.ReadFile(filepath.Join(out, "original-"+n+".bson"))
		write(t, filepath.Join(restored, n+".bson"), b)
		b, _ = os.ReadFile(filepath.Join(out, "indexes-"+n+".bson"))
		write(t, filepath.Join(restored, n+".indexes.bson"), b)
	}
	rollback, e := Convert(restored, freshOutput(t))
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(m)
	b, _ := json.Marshal(rollback)
	if !bytes.Equal(a, b) {
		t.Fatal("restore not deterministic")
	}
	assertSource(t, restored, data, idx)
}

func TestInterruptionsNeverAcceptPartialOutput(t *testing.T) {
	src, data, idx := fixture(t)
	// A minimal snapshot exercises every I/O boundary without repeating large
	// all-collection durability writes hundreds of times.
	for name := range data {
		if name != "hosts" {
			delete(data, name)
			delete(idx, name)
			if e := os.Remove(filepath.Join(src, name+".bson")); e != nil {
				t.Fatal(e)
			}
			if e := os.Remove(filepath.Join(src, name+".indexes.bson")); e != nil {
				t.Fatal(e)
			}
		}
	}
	saveFixture(t, src, data, idx)
	var steps []string
	if _, e := convert(src, freshOutput(t), func(s string) error { steps = append(steps, s); return nil }); e != nil {
		t.Fatal(e)
	}
	for stop := range steps {
		t.Run(steps[stop], func(t *testing.T) {
			out := freshOutput(t)
			i := 0
			_, e := convert(src, out, func(string) error {
				at := i
				i++
				if at == stop {
					return errors.New("injected write/durability interruption")
				}
				return nil
			})
			if e == nil {
				t.Fatal("injection missed")
			}
			_, verify := Verify(out)
			if steps[stop] == "rename:manifest" || steps[stop] == "sync:committed" {
				if verify != nil {
					t.Fatal("complete committed bundle failed verification", verify)
				}
			} else {
				if verify == nil {
					t.Fatal("partial output marked ready")
				}
				if _, e = Convert(src, out); e == nil {
					t.Fatal("partial output overwritten")
				}
			}
			assertSource(t, src, data, idx)
		})
	}
}
func TestRefusesInvalidSnapshotBeforeCreatingOutput(t *testing.T) {
	cases := map[string]func(*testing.T, string, map[string][]byte, map[string][]byte){
		"trailing": func(t *testing.T, _ string, d, i map[string][]byte) { d["vms"] = append(d["vms"], 1) },
		"duplicate nested": func(t *testing.T, _ string, d, i map[string][]byte) {
			d["future_collection"] = raw(t, bson.D{{Key: "_id", Value: "a"}, {Key: "x", Value: bson.D{{Key: "x", Value: 1}, {Key: "x", Value: 2}}}})
		},
		"duplicate id": func(t *testing.T, _ string, d, i map[string][]byte) { d["hosts"] = append(d["hosts"], d["hosts"]...) },
		"missing host": func(t *testing.T, _ string, d, i map[string][]byte) { d["hosts"] = nil },
		"wrong type": func(t *testing.T, _ string, d, i map[string][]byte) {
			d["sessions"] = raw(t, bson.D{{Key: "_id", Value: "a"}, {Key: "expiresat", Value: int64(1)}})
		},
		"conflict": func(t *testing.T, _ string, d, i map[string][]byte) {
			d["vms"] = raw(t, bson.D{{Key: "_id", Value: "a"}, {Key: "hostid", Value: "host-1"}, {Key: "host_id", Value: "other"}})
		},
		"case alias": func(t *testing.T, _ string, d, i map[string][]byte) {
			d["vms"] = raw(t, bson.D{{Key: "_id", Value: "a"}, {Key: "HostID", Value: "host-1"}})
		},
		"missing required": func(t *testing.T, _ string, d, i map[string][]byte) {
			d["sessions"] = raw(t, bson.D{{Key: "_id", Value: "a"}})
		},
		"unsupported regex": func(t *testing.T, _ string, d, i map[string][]byte) {
			d["future_collection"] = raw(t, bson.D{{Key: "_id", Value: "a"}, {Key: "regex", Value: primitive.Regex{Pattern: "x"}}})
		},
		"alias index": func(t *testing.T, _ string, d, i map[string][]byte) {
			i["vms"] = raw(t, bson.D{{Key: "name", Value: "old"}, {Key: "key", Value: bson.D{{Key: "hostid", Value: int32(1)}}}})
		},
		"incompatible ttl": func(t *testing.T, _ string, d, i map[string][]byte) {
			i["sessions"] = raw(t, bson.D{{Key: "name", Value: "expiry"}, {Key: "key", Value: bson.D{{Key: "expires_at", Value: int32(1)}}}, {Key: "expireAfterSeconds", Value: int32(10)}})
		},
		"collation": func(t *testing.T, _ string, d, i map[string][]byte) {
			i["hosts"] = raw(t, bson.D{{Key: "name", Value: "other"}, {Key: "key", Value: bson.D{{Key: "_id", Value: int32(1)}}}, {Key: "collation", Value: bson.D{{Key: "locale", Value: "en"}}}})
		},
		"unique collision": func(t *testing.T, _ string, d, i map[string][]byte) {
			d["users"] = append(raw(t, bson.D{{Key: "_id", Value: "a"}, {Key: "username", Value: "same"}}), raw(t, bson.D{{Key: "_id", Value: "b"}, {Key: "username", Value: "same"}})...)
			i["users"] = raw(t, bson.D{{Key: "name", Value: "username"}, {Key: "key", Value: bson.D{{Key: "username", Value: int32(1)}}}, {Key: "unique", Value: true}, {Key: "sparse", Value: true}})
		},
		"unique numeric": func(t *testing.T, _ string, d, i map[string][]byte) {
			d["users"] = raw(t, bson.D{{Key: "_id", Value: "a"}, {Key: "username", Value: 1}})
			i["users"] = raw(t, bson.D{{Key: "name", Value: "username"}, {Key: "key", Value: bson.D{{Key: "username", Value: int32(1)}}}, {Key: "unique", Value: true}, {Key: "sparse", Value: true}})
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			src, d, i := fixture(t)
			change(t, src, d, i)
			saveFixture(t, src, d, i)
			out := freshOutput(t)
			if _, e := Convert(src, out); e == nil {
				t.Fatal("accepted invalid snapshot")
			}
			if _, e := os.Stat(out); !os.IsNotExist(e) {
				t.Fatal("output created before validation")
			}
			assertSource(t, src, d, i)
		})
	}
}
func TestBundleTamperingRefused(t *testing.T) {
	for _, name := range []string{"normalized-vms.bson", "original-perf_history.bson", "indexes-sessions.bson", "ledger.json", "source.json", "manifest.json", "extra"} {
		t.Run(name, func(t *testing.T) {
			src, _, _ := fixture(t)
			out := freshOutput(t)
			if _, e := Convert(src, out); e != nil {
				t.Fatal(e)
			}
			p := filepath.Join(out, name)
			b, _ := os.ReadFile(p)
			write(t, p, append(b, 1))
			if _, e := Verify(out); e == nil {
				t.Fatal("tamper accepted")
			}
			if _, e := History(out); e == nil {
				t.Fatal("reader accepted tamper")
			}
			if _, e := Convert(src, out); e == nil {
				t.Fatal("tamper overwritten")
			}
		})
	}
}
func TestFilesystemAndManifestRefusals(t *testing.T) {
	for _, kind := range []string{"extra", "symlink", "hardlink", "duplicate JSON", "case JSON", "unknown schema", "source changed", "output nested", "output alias", "output exists"} {
		t.Run(kind, func(t *testing.T) {
			src, _, _ := fixture(t)
			out := freshOutput(t)
			switch kind {
			case "extra":
				write(t, filepath.Join(src, "extra"), nil)
			case "symlink":
				p := filepath.Join(src, "hosts.bson")
				b, _ := os.ReadFile(p)
				external := filepath.Join(t.TempDir(), "external")
				write(t, external, b)
				if e := os.Remove(p); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(external, p); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				if e := os.Link(filepath.Join(src, "hosts.bson"), filepath.Join(t.TempDir(), "alias")); e != nil {
					t.Fatal(e)
				}
			case "duplicate JSON":
				p := filepath.Join(src, "snapshot.json")
				b, _ := os.ReadFile(p)
				write(t, p, append([]byte(`{"contract":"other",`), b[1:]...))
			case "case JSON":
				p := filepath.Join(src, "snapshot.json")
				b, _ := os.ReadFile(p)
				write(t, p, bytes.Replace(b, []byte(`"contract"`), []byte(`"Contract"`), 1))
			case "unknown schema":
				p := filepath.Join(src, "snapshot.json")
				b, _ := os.ReadFile(p)
				write(t, p, bytes.Replace(b, []byte("synthetic-full-bson-v1"), []byte("real-data"), 1))
			case "source changed":
				p := filepath.Join(src, "hosts.bson")
				b, _ := os.ReadFile(p)
				write(t, p, append(b, 1))
			case "output nested":
				out = filepath.Join(src, ".nested")
			case "output alias":
				if e := os.Symlink(src, out); e != nil {
					t.Fatal(e)
				}
			case "output exists":
				if e := os.Mkdir(out, 0700); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := Convert(src, out); e == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}
func TestAliasesPreserveOrderAndOptionalAbsence(t *testing.T) {
	for _, collection := range []string{"vms", "sessions", "migrations", "audit_events"} {
		m := mappings[collection]
		var value any = "host-1"
		if collection == "sessions" {
			value = primitive.DateTime(-1)
		}
		if collection == "migrations" {
			value = int64(1)
		}
		for _, order := range []int{0, 1, 2} {
			d := bson.D{{Key: "_id", Value: "a"}, {Key: "nested", Value: bson.D{{Key: m.alias, Value: "untouched"}}}}
			if order == 0 {
				d = append(d, bson.E{Key: m.alias, Value: value})
			}
			if order == 1 {
				d = append(d, bson.E{Key: m.alias, Value: value}, bson.E{Key: m.canonical, Value: value})
			}
			if order == 2 {
				d = append(d, bson.E{Key: m.canonical, Value: value}, bson.E{Key: m.alias, Value: value})
			}
			r := raw(t, d)
			n, _, e := normalize(collection, r)
			if e != nil {
				t.Fatal(e)
			}
			if e = verifyElements(collection, r, n); e != nil {
				t.Fatal(e)
			}
			again, changes, e := normalize(collection, n)
			if e != nil || len(changes) != 0 || !bytes.Equal(n, again) {
				t.Fatal("not idempotent")
			}
		}
	}
	r := raw(t, bson.D{{Key: "_id", Value: "a"}})
	n, _, e := normalize("audit_events", r)
	if e != nil || !bytes.Equal(r, n) {
		t.Fatal("optional absent altered")
	}
}
