package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"kvm-center/internal/models"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func passthroughFixture(t *testing.T) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(bson.D{{Key: "_id", Value: tagFixtureUUID}, {Key: "uuid", Value: tagFixtureUUID}, {Key: "host_id", Value: "fixture"}, {Key: "name", Value: "synthetic-passthrough"}, {Key: "haspassthrough", Value: true}, {Key: "pcidevices", Value: bson.A{bson.D{{Key: "sourceaddr", Value: "0000:05:00.0"}, {Key: "hostdesc", Value: "synthetic device"}}}}, {Key: "opaque", Value: primitive.Binary{Subtype: 0x80, Data: []byte{0, 255, 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func passthroughSnapshot(t *testing.T) *persistData {
	t.Helper()
	raw := passthroughFixture(t)
	var v models.VM
	if err := bson.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return &persistData{VMs: map[string][]*models.VM{"fixture": {&v}}, Hosts: map[string]*models.Host{"fixture": {ID: "fixture"}}, EntityOverlay: &entityOverlay{Version: 8, Collections: map[string]map[string][]byte{mcoVMs: {tagFixtureUUID: raw}}}}
}
func TestPassthroughEvidenceStrictSemantics(t *testing.T) {
	raw := passthroughFixture(t)
	good, err := decodePassthroughEvidence(raw, tagFixtureUUID)
	if err != nil || good.HasPassthrough == nil || !*good.HasPassthrough || len(good.PCIDevices) != 1 || good.NativeVerified {
		t.Fatal(good, err)
	}
	cases := map[string]func(bson.D) bson.D{
		"alias":          func(d bson.D) bson.D { return append(d, bson.E{Key: "has_passthrough", Value: true}) },
		"flag type":      func(d bson.D) bson.D { d[4].Value = "true"; return d },
		"null flag":      func(d bson.D) bson.D { d[4].Value = nil; return d },
		"false conflict": func(d bson.D) bson.D { d[4].Value = false; return d },
		"duplicate":      func(d bson.D) bson.D { return append(d, d[4]) },
		"pci alias":      func(d bson.D) bson.D { d[5].Key = "pci_devices"; return d },
		"pci type":       func(d bson.D) bson.D { d[5].Value = "device"; return d },
		"pci duplicate": func(d bson.D) bson.D {
			d[5].Value = bson.A{bson.D{{Key: "sourceaddr", Value: "0000:05:00.0"}, {Key: "hostdesc", Value: "a"}}, bson.D{{Key: "sourceaddr", Value: "0000:05:00.0"}, {Key: "hostdesc", Value: "b"}}}
			return d
		},
		"pci unknown": func(d bson.D) bson.D {
			d[5].Value = bson.A{bson.D{{Key: "sourceaddr", Value: "0000:05:00.0"}, {Key: "hostdesc", Value: "a"}, {Key: "unknown", Value: true}}}
			return d
		},
		"pci invalid": func(d bson.D) bson.D {
			d[5].Value = bson.A{bson.D{{Key: "sourceaddr", Value: "0000:05:20.0"}, {Key: "hostdesc", Value: "a"}}}
			return d
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			var d bson.D
			_ = bson.Unmarshal(raw, &d)
			b, _ := bson.Marshal(edit(d))
			if _, err := decodePassthroughEvidence(b, tagFixtureUUID); err == nil {
				t.Fatal("unsupported accepted")
			}
		})
	}
	for _, flag := range []any{nil, false, true} {
		for _, status := range []string{"missing", "null", "present"} {
			d := bson.D{{Key: "_id", Value: tagFixtureUUID}, {Key: "uuid", Value: tagFixtureUUID}}
			if flag != nil {
				d = append(d, bson.E{Key: "haspassthrough", Value: flag})
			}
			if status == "null" {
				d = append(d, bson.E{Key: "pcidevices", Value: nil})
			}
			if status == "present" {
				d = append(d, bson.E{Key: "pcidevices", Value: bson.A{}})
			}
			b, _ := bson.Marshal(d)
			v, err := decodePassthroughEvidence(b, tagFixtureUUID)
			if err != nil || v.PCIRecordsStatus != status || (v.HasPassthrough == nil) != (flag == nil) {
				t.Fatal(v, err)
			}
		}
	}
}
func TestPassthroughEvidenceJSONRecoveryAndAuthorization(t *testing.T) {
	ctx := context.Background()
	b := NewJSONBackend(filepath.Join(t.TempDir(), "state.json"))
	d := passthroughSnapshot(t)
	if err := b.Save(ctx, d); err != nil {
		t.Fatal(err)
	}
	s := NewStoreWithBackend(b, "")
	s.saveWG.Wait()
	view, err := s.PassthroughEvidence(ctx, tagFixtureUUID)
	if err != nil {
		t.Fatal(err)
	}
	view.PCIDevices[0].HostDesc = "caller mutation"
	report := &models.TelemetryPayload{HostID: "fixture", VMs: []models.VM{*d.VMs["fixture"][0]}}
	if err = s.ApplyTelemetry(report); err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err = s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	again, err := s.PassthroughEvidence(ctx, tagFixtureUUID)
	if err != nil || !*again.HasPassthrough || again.PCIDevices[0].HostDesc != "synthetic device" {
		t.Fatal(again, err)
	}
	raw, err := s.ExportSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot persistData
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	old := passthroughFixture(t)
	saved := bson.Raw(snapshot.EntityOverlay.Collections[mcoVMs][tagFixtureUUID])
	for _, key := range []string{"haspassthrough", "pcidevices", "opaque"} {
		if !equalRaw(old.Lookup(key), saved.Lookup(key)) {
			t.Fatal("original changed", key)
		}
	}
	restored := NewJSONBackend(filepath.Join(t.TempDir(), "restored.json"))
	if err = restored.Save(ctx, &snapshot); err != nil {
		t.Fatal(err)
	}
	s = NewStoreWithBackend(restored, "")
	s.saveWG.Wait()
	got, err := s.PassthroughEvidence(ctx, tagFixtureUUID)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatal("recovery", err)
	}
	u := &models.User{ID: "reader", Enabled: true, RoleIDs: []string{"reader"}}
	s.mu.Lock()
	s.users[u.ID] = u
	s.roles["reader"] = &models.Role{ID: "reader", Permissions: []string{"vm.view"}}
	s.mu.Unlock()
	for _, tc := range []struct {
		auth   bool
		method string
		want   int
	}{{false, "GET", 401}, {true, "GET", 200}, {true, "POST", 405}} {
		r := httptest.NewRequest(tc.method, "/api/vms/"+tagFixtureUUID+"/passthrough-evidence", nil)
		if tc.auth {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
		}
		w := httptest.NewRecorder()
		vmDetailRoute(s).ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
		if bytes.Contains(w.Body.Bytes(), []byte("opaque")) {
			t.Fatal("opaque leaked")
		}
	}
	s.mu.Lock()
	s.vms["other"] = []*models.VM{{UUID: strings.ToUpper(tagFixtureUUID)}}
	s.mu.Unlock()
	if _, err = s.PassthroughEvidence(ctx, tagFixtureUUID); err == nil {
		t.Fatal("ambiguous accepted")
	}
}

func TestPassthroughOpaquePCIEditRefused(t *testing.T) {
	d := passthroughSnapshot(t)
	var doc bson.D
	_ = bson.Unmarshal(d.EntityOverlay.Collections[mcoVMs][tagFixtureUUID], &doc)
	doc[5].Value = bson.A{bson.D{{Key: "sourceaddr", Value: "0000:05:00.0"}, {Key: "hostdesc", Value: "synthetic device"}, {Key: "opaque", Value: primitive.Binary{Subtype: 0x80, Data: []byte{9, 0, 255}}}}}
	raw, _ := bson.Marshal(doc)
	d.EntityOverlay.Collections[mcoVMs][tagFixtureUUID] = raw
	if _, err := decodePassthroughEvidence(raw, tagFixtureUUID); err == nil {
		t.Fatal("unknown PCI semantics claimed supported")
	}
	b := NewJSONBackend(filepath.Join(t.TempDir(), "state.json"))
	if err := b.Save(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	d.VMs["fixture"][0].PCIDevices[0].HostDesc = "changed"
	if err := b.Save(t.Context(), d); err == nil {
		t.Fatal("opaque PCI edit silently lost")
	}
	got, err := b.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	retained := bson.Raw(got.EntityOverlay.Collections[mcoVMs][tagFixtureUUID])
	if !equalRaw(bson.Raw(raw).Lookup("pcidevices"), retained.Lookup("pcidevices")) {
		t.Fatal("refusal changed original")
	}
}

func TestPassthroughEvidenceMalformedArrayAndTrailingBytes(t *testing.T) {
	for _, key := range []string{"1", "00"} {
		d := passthroughFixture(t)
		var doc bson.D
		_ = bson.Unmarshal(d, &doc)
		array, _ := bson.Marshal(bson.D{{Key: key, Value: bson.D{{Key: "sourceaddr", Value: "0000:05:00.0"}, {Key: "hostdesc", Value: "synthetic"}}}})
		doc[5].Value = bson.RawValue{Type: bsontype.Array, Value: array}
		raw, _ := bson.Marshal(doc)
		if _, err := decodePassthroughEvidence(raw, tagFixtureUUID); err == nil {
			t.Fatal("noncanonical array accepted", key)
		}
	}
	raw := append(passthroughFixture(t), 0)
	if _, err := decodePassthroughEvidence(raw, tagFixtureUUID); err == nil {
		t.Fatal("trailing bytes accepted")
	}
}
