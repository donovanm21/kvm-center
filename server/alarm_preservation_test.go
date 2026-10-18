package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"kvm-center/internal/models"
	"math"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func alarmFixture(t *testing.T, id string) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(bson.D{{Key: "_id", Value: id}, {Key: "id", Value: id}, {Key: "name", Value: "Synthetic CPU alarm"}, {Key: "enabled", Value: true}, {Key: "metric", Value: alarmHostCPU}, {Key: "threshold", Value: float64(80)}, {Key: "forseconds", Value: int32(0)}, {Key: "severity", Value: "warning"}, {Key: "hosts", Value: bson.A{"FIXTURE-HOST"}}, {Key: "vms", Value: bson.A{"ignored-vm-scope"}}, {Key: "createdat", Value: "2020-01-02T03:04:05Z"}, {Key: "updatedat", Value: "2020-01-02T03:04:05Z"}, {Key: "opaque", Value: primitive.NewDecimal128(109, 113)}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestAlarmPreservationWire(t *testing.T) {
	raw := alarmFixture(t, "original")
	if err := validateAlarmBSON(raw); err != nil {
		t.Fatal(err)
	}
	var a models.Alarm
	bson.Unmarshal(raw, &a)
	a.Name = "Edited alarm"
	d := &persistData{Alarms: []*models.Alarm{&a}, EntityOverlay: &entityOverlay{Version: 8, Collections: map[string]map[string][]byte{mcoAlarms: {a.ID: raw}}}}
	wire, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back persistData
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatal(err)
	}
	got := bson.Raw(back.EntityOverlay.Collections[mcoAlarms][a.ID])
	if !equalRaw(got.Lookup("opaque"), raw.Lookup("opaque")) || back.Alarms[0].Name != a.Name {
		t.Fatal("known alarm write lost values")
	}
	if json.Unmarshal(bytes.Replace(wire, []byte(`"version":8`), []byte(`"version":7`), 1), &back) == nil {
		t.Fatal("old overlay accepted alarms")
	}
	d.Alarms = nil
	wire, err = json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &back); err != nil || len(back.EntityOverlay.Collections[mcoAlarms]) != 0 {
		t.Fatal("deleted alarm resurrected", err)
	}
}
func TestAlarmStrictAdmission(t *testing.T) {
	for _, kind := range []string{"identity", "alias", "case", "duplicate", "integer-threshold", "null-duration", "scope-type", "derived", "unsupported-disabled", "nan", "infinite", "too-low", "duration", "severity", "timestamp"} {
		t.Run(kind, func(t *testing.T) {
			var doc bson.D
			bson.Unmarshal(alarmFixture(t, "original"), &doc)
			set := func(key string, value any) {
				for i := range doc {
					if doc[i].Key == key {
						doc[i].Value = value
						return
					}
				}
				doc = append(doc, bson.E{Key: key, Value: value})
			}
			switch kind {
			case "identity":
				set("_id", "different")
			case "alias":
				set("for_seconds", int32(0))
			case "case":
				set("Metric", alarmHostMemory)
			case "duplicate":
				doc = append(doc, bson.E{Key: "id", Value: "original"})
			case "integer-threshold":
				set("threshold", int32(80))
			case "null-duration":
				set("forseconds", nil)
			case "scope-type":
				set("hosts", bson.A{int32(7)})
			case "derived":
				set("firing_on", bson.A{"host"})
			case "unsupported-disabled":
				set("metric", "host.offline")
				set("enabled", false)
			case "nan":
				set("threshold", math.NaN())
			case "infinite":
				set("threshold", math.Inf(1))
			case "too-low":
				set("threshold", float64(5))
			case "duration":
				set("forseconds", int32(-1))
			case "severity":
				set("severity", "future")
			case "timestamp":
				set("createdat", "unknown")
			}
			raw, _ := bson.Marshal(doc)
			if validateAlarmBSON(raw) == nil {
				t.Fatal("unsupported alarm accepted")
			}
		})
	}
}
func TestResourceAlarmOperationalSemantics(t *testing.T) {
	s := NewStoreWithBackend(nil, t.TempDir()+"/state.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	var a models.Alarm
	bson.Unmarshal(alarmFixture(t, "cpu"), &a)
	a.ForSeconds = 60
	memory := a
	memory.ID = "memory"
	memory.Metric = alarmHostMemory
	memory.ForSeconds = 0
	s.hosts["fixture-host"] = &models.Host{ID: "fixture-host", Status: models.HostOffline, CPUUsage: 80, MemUsage: 80}
	s.hosts["excluded"] = &models.Host{ID: "excluded", CPUUsage: 99, MemUsage: 99}
	if err := s.SetResourceAlarms([]*models.Alarm{&a, &memory}); err != nil {
		t.Fatal(err)
	}
	a.Hosts[0] = "mutated" // Inputs are detached.
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	events := s.evaluateResourceAlarms(now)
	if len(events) != 1 || events[0].Detail["alarm_id"] != "memory" || events[0].Type != "alarm.fired" {
		t.Fatal("memory trigger/offline/scope semantics", events)
	}
	if got := s.evaluateResourceAlarms(now.Add(59 * time.Second)); len(got) != 0 {
		t.Fatal("fired before duration or duplicated episode")
	}
	events = s.evaluateResourceAlarms(now.Add(time.Minute))
	if len(events) != 1 || events[0].Detail["alarm_id"] != "cpu" {
		t.Fatal("duration trigger failed")
	}
	s.mu.Lock()
	s.hosts["fixture-host"].CPUUsage = 75.01
	s.hosts["fixture-host"].MemUsage = 75.01
	s.mu.Unlock()
	if len(s.evaluateResourceAlarms(now.Add(61*time.Second))) != 0 {
		t.Fatal("cleared above hysteresis boundary")
	}
	s.mu.Lock()
	s.hosts["fixture-host"].CPUUsage = 75
	s.hosts["fixture-host"].MemUsage = 75
	s.mu.Unlock()
	events = s.evaluateResourceAlarms(now.Add(62 * time.Second))
	if len(events) != 2 || events[0].Type != "alarm.cleared" || events[1].Type != "alarm.cleared" {
		t.Fatal("hysteresis equality did not clear")
	}
	// A broken breach restarts its hold; it cannot accumulate disjoint samples.
	s.mu.Lock()
	s.hosts["fixture-host"].CPUUsage = 90
	s.mu.Unlock()
	s.evaluateResourceAlarms(now.Add(70 * time.Second))
	s.mu.Lock()
	s.hosts["fixture-host"].CPUUsage = 70
	s.mu.Unlock()
	s.evaluateResourceAlarms(now.Add(80 * time.Second))
	s.mu.Lock()
	s.hosts["fixture-host"].CPUUsage = 90
	s.mu.Unlock()
	s.evaluateResourceAlarms(now.Add(90 * time.Second))
	if len(s.evaluateResourceAlarms(now.Add(149*time.Second))) != 0 {
		t.Fatal("duration accumulated across clear")
	}
	if len(s.evaluateResourceAlarms(now.Add(150*time.Second))) != 1 {
		t.Fatal("new episode did not fire")
	}
	got := s.GetResourceAlarms()
	got[0].Hosts[0] = "escaped"
	if s.GetResourceAlarms()[0].Hosts[0] == "escaped" {
		t.Fatal("mutable scope escaped")
	}
	unchanged := cloneAlarms(s.alarms)
	if err := s.SetResourceAlarms(unchanged); err != nil {
		t.Fatal(err)
	}
	if len(s.GetResourceAlarms()[0].FiringOn) != 1 {
		t.Fatal("unchanged config reset episode")
	}
	bad := cloneAlarms(s.alarms)
	bad[0].Metric = "host.offline"
	before := s.GetResourceAlarms()
	if s.SetResourceAlarms(bad) == nil || !reflect.DeepEqual(before, s.GetResourceAlarms()) {
		t.Fatal("refused edit mutated config or state")
	}
	edit := cloneAlarms(s.alarms)
	edit[0].Threshold = 95
	if err := s.SetResourceAlarms(edit); err != nil {
		t.Fatal(err)
	}
	if len(s.GetResourceAlarms()[0].FiringOn) != 0 {
		t.Fatal("edited definition retained stale state")
	}
	edit[0].Enabled = false
	if err := s.SetResourceAlarms(edit); err != nil {
		t.Fatal(err)
	}
	if len(s.evaluateResourceAlarms(now.Add(300*time.Second))) != 0 {
		t.Fatal("disabled alarm fired")
	}
	if err := s.SetResourceAlarms(nil); err != nil {
		t.Fatal(err)
	}
	if len(s.alarmStates) != 0 {
		t.Fatal("deleted alarm retained state")
	}
}

func TestAlarmEmptyScopeArraysAndJSONNullRefusal(t *testing.T) {
	var a models.Alarm
	bson.Unmarshal(alarmFixture(t, "original"), &a)
	a.Hosts = []string{}
	a.VMs = []string{}
	clone := cloneAlarms([]*models.Alarm{&a})[0]
	if clone.Hosts == nil || clone.VMs == nil {
		t.Fatal("empty arrays collapsed")
	}
	raw, err := json.Marshal(&persistData{Alarms: []*models.Alarm{clone}})
	if err != nil {
		t.Fatal(err)
	}
	var d persistData
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Alarms[0].Hosts == nil || d.Alarms[0].VMs == nil {
		t.Fatal("snapshot lost empty array shape")
	}
	for _, field := range []string{"enabled", "for_seconds", "threshold", "severity", "created_at"} {
		wire, _ := json.Marshal(&a)
		var obj map[string]json.RawMessage
		json.Unmarshal(wire, &obj)
		obj[field] = json.RawMessage("null")
		wire, _ = json.Marshal(obj)
		if json.Unmarshal(wire, &a) == nil {
			t.Fatal("null scalar accepted", field)
		}
		if json.Unmarshal(append(append([]byte(`{"alarms":[`), wire...), []byte(`]}`)...), &d) == nil {
			t.Fatal("snapshot accepted null scalar", field)
		}
	}
}

func TestAlarmAPIAndCapacityRefusalPreservesState(t *testing.T) {
	s := NewStoreWithBackend(nil, t.TempDir()+"/state.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	var a models.Alarm
	bson.Unmarshal(alarmFixture(t, "original"), &a)
	if err := s.SetResourceAlarms([]*models.Alarm{&a}); err != nil {
		t.Fatal(err)
	}
	before := s.GetResourceAlarms()
	for _, field := range []string{"enabled", "for_seconds"} {
		wire, _ := json.Marshal(a)
		var obj map[string]json.RawMessage
		json.Unmarshal(wire, &obj)
		obj[field] = json.RawMessage("null")
		wire, _ = json.Marshal([]map[string]json.RawMessage{obj})
		w := httptest.NewRecorder()
		handleResourceAlarms(s)(w, httptest.NewRequest("PUT", "/api/alarms", bytes.NewReader(wire)))
		if w.Code != 400 || !reflect.DeepEqual(before, s.GetResourceAlarms()) {
			t.Fatalf("null %s mutated state: %d", field, w.Code)
		}
	}
	oversized := make([]*models.Alarm, alarmAdmissionMax+1)
	for i := range oversized {
		c := a
		c.ID = fmt.Sprintf("alarm-%d", i)
		oversized[i] = &c
	}
	if s.SetResourceAlarms(oversized) == nil || !reflect.DeepEqual(before, s.GetResourceAlarms()) {
		t.Fatal("capacity refusal mutated state")
	}
}
