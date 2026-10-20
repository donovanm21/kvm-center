package main

import (
	"bytes"
	"encoding/json"
	"kvm-center/internal/offlinestaging"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/x/bsonx/bsoncore"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/models"
)

func compositionEntities(t *testing.T, d *persistData, docs map[string][]byte) {
	t.Helper()
	d.EntityOverlay = &entityOverlay{Version: 8, Collections: map[string]map[string][]byte{}}
	d.Hosts = map[string]*models.Host{}
	d.VMs = map[string][]*models.VM{}
	d.Storage = map[string][]*models.StoragePool{}
	d.VMNotes = map[string]string{}
	d.GuestData = map[string]*guestEntry{}
	d.Users = map[string]*models.User{}
	d.Groups = map[string]*models.Group{}
	d.Roles = map[string]*models.Role{}
	d.RemovedHosts = map[string]*models.RemovedHost{}
	d.Sessions = map[string]*models.Session{}
	if d.APITokens == nil {
		d.APITokens = map[string]*models.APIToken{}
	}
	decode := func(raw []byte, v any) {
		t.Helper()
		if err := bson.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
	}
	for col, all := range docs {
		d.EntityOverlay.Collections[col] = map[string][]byte{}
		for len(all) > 0 {
			doc, rest, ok := bsoncore.ReadDocument(all)
			if !ok {
				t.Fatal("invalid component stream")
			}
			raw := []byte(doc)
			all = rest
			id := bson.Raw(raw).Lookup("_id").StringValue()
			d.EntityOverlay.Collections[col][id] = append([]byte(nil), raw...)
			switch col {
			case mcoHosts:
				v := new(models.Host)
				decode(raw, v)
				d.Hosts[v.ID] = v
			case mcoVMs:
				v := new(models.VM)
				decode(raw, v)
				d.VMs[v.HostID] = append(d.VMs[v.HostID], v)
			case mcoStorage:
				v := new(models.StoragePool)
				decode(raw, v)
				d.Storage[v.HostID] = append(d.Storage[v.HostID], v)
			case mcoVMNotes:
				v := new(entityVMNote)
				decode(raw, v)
				d.VMNotes[v.UUID] = v.Text
			case mcoUsers:
				v := new(models.User)
				decode(raw, v)
				d.Users[v.ID] = v
			case mcoGroups:
				v := new(models.Group)
				decode(raw, v)
				d.Groups[v.ID] = v
			case mcoRoles:
				v := new(models.Role)
				decode(raw, v)
				d.Roles[v.ID] = v
			case mcoRemovedHosts:
				v := new(models.RemovedHost)
				decode(raw, v)
				d.RemovedHosts[v.HostID] = v
			case mcoGuestData:
				v := new(entityGuestReport)
				decode(raw, v)
				d.GuestData[v.UUID] = &v.Entry
			case mcoAuditEvents:
				v := new(models.AuditEvent)
				decode(raw, v)
				d.Audit = append(d.Audit, v)
			case mcoAPITokens:
				v := new(models.APIToken)
				decode(raw, v)
				d.APITokens[v.ID] = v
			case mcoSessions:
				v := new(models.Session)
				decode(raw, v)
				d.Sessions[v.Token] = v
			case mcoAlarms:
				v := new(models.Alarm)
				decode(raw, v)
				d.Alarms = append(d.Alarms, v)
			default:
				t.Fatalf("unaccounted composition collection %s", col)
			}
		}
	}
}
func TestSupportedCompositionEntityWire(t *testing.T) {
	fixture := legacyfixture.GenerateCompositionEntities()
	d := &persistData{}
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	src := filepath.Join(root, "source")
	if e = os.Mkdir(src, 0700); e != nil {
		t.Fatal(e)
	}
	if e = legacyfixture.Write(src, fixture); e != nil {
		t.Fatal(e)
	}
	stage := filepath.Join(root, "stage")
	if _, e = offlinestaging.Convert(src, stage); e != nil {
		t.Fatal(e)
	}
	docs := map[string][]byte{}
	for col := range fixture.Documents {
		raw, e := os.ReadFile(filepath.Join(stage, "normalized-"+col+".bson"))
		if e != nil {
			t.Fatal(e)
		}
		docs[col] = raw
	}
	compositionEntities(t, d, docs)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back persistData
	if err = json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.EntityOverlay.Collections) != 13 {
		t.Fatal("composition lost a collection")
	}
	for col, all := range fixture.Documents {
		for len(all) > 0 {
			doc, rest, ok := bsoncore.ReadDocument(all)
			if !ok {
				t.Fatal("invalid BSON")
			}
			old := []byte(doc)
			all = rest
			id := bson.Raw(old).Lookup("_id").StringValue()
			got := back.EntityOverlay.Collections[col][id]
			if !equalRaw(bson.Raw(old).Lookup("opaque"), bson.Raw(got).Lookup("opaque")) {
				t.Fatalf("opaque lost %s", col)
			}
		}
	}
	statePath := filepath.Join(root, "state.json")
	backend := NewJSONBackend(statePath)
	if err := backend.Save(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	store := NewStoreWithBackend(backend, statePath)
	t.Cleanup(func() { store.saveWG.Wait() })
	store.SetVMNotes(legacyfixture.VMID, "composition durable note")
	alarms := cloneAlarms(d.Alarms)
	alarms[0].Name = "composition durable alarm"
	requestBody, _ := json.Marshal(alarms)
	response := httptest.NewRecorder()
	handleResourceAlarms(store)(response, httptest.NewRequest("PUT", "/api/alarms", bytes.NewReader(requestBody)))
	if response.Code != 200 {
		t.Fatalf("composition alarm edit: %d %s", response.Code, response.Body.String())
	}
	for _, field := range []string{"name", "enabled", "metric", "threshold", "for_seconds", "severity"} {
		for _, missing := range []bool{false, true} {
			var records []map[string]json.RawMessage
			if err := json.Unmarshal(requestBody, &records); err != nil {
				t.Fatal(err)
			}
			if missing {
				delete(records[0], field)
			} else {
				records[0][field] = json.RawMessage("null")
			}
			invalid, _ := json.Marshal(records)
			w := httptest.NewRecorder()
			handleResourceAlarms(store)(w, httptest.NewRequest("PUT", "/api/alarms", bytes.NewReader(invalid)))
			if w.Code != 400 {
				t.Fatalf("ambiguous alarm %s accepted", field)
			}
		}
	}
	for _, field := range []string{"unknown_future", "firing_on"} {
		var records []map[string]json.RawMessage
		if err := json.Unmarshal(requestBody, &records); err != nil {
			t.Fatal(err)
		}
		records[0][field] = json.RawMessage("[]")
		invalid, _ := json.Marshal(records)
		w := httptest.NewRecorder()
		handleResourceAlarms(store)(w, httptest.NewRequest("PUT", "/api/alarms", bytes.NewReader(invalid)))
		if w.Code != 400 {
			t.Fatal("unsupported alarm field accepted")
		}
	}
	if bytes.Contains(raw, []byte("unknown_future")) {
		t.Fatal("adversarial namespace entered supported composition")
	}
}
