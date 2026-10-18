package main

import (
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
	"reflect"
	"strings"
	"testing"
	"time"
)

func remainingAlarmFixture(t *testing.T, id, metric string) bson.Raw {
	t.Helper()
	var doc bson.D
	bson.Unmarshal(alarmFixture(t, id), &doc)
	for i := range doc {
		switch doc[i].Key {
		case "metric":
			doc[i].Value = metric
		case "threshold":
			switch metric {
			case alarmHostOffline:
				doc[i].Value = float64(60)
			case alarmPoolUsed:
				doc[i].Value = float64(80)
			default:
				doc[i].Value = float64(0)
			}
		case "hosts":
			if metric == alarmVMStopped {
				doc[i].Value = bson.A{"intentionally-ignored-host"}
			}
		case "vms":
			doc[i].Value = bson.A{"fixture-vm"}
		}
	}
	raw, err := bson.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func remainingAlarmSet(t *testing.T) []*models.Alarm {
	out := []*models.Alarm{}
	for _, metric := range []string{alarmHostOffline, alarmPoolUsed, alarmVMStopped, alarmAgentVersion} {
		var a models.Alarm
		bson.Unmarshal(remainingAlarmFixture(t, metric, metric), &a)
		out = append(out, &a)
	}
	return out
}
func seedAlarmMetricInventory(s *Store, now time.Time) {
	s.hosts["fixture-host"] = &models.Host{ID: "fixture-host", Status: models.HostOnline, LastSeen: now.Add(-61 * time.Second), AgentVersion: "synthetic-different-version"}
	s.storage["fixture-host"] = []*models.StoragePool{{Name: "pool", HostID: "fixture-host", CapacityGB: 100, AvailableGB: 10}}
	s.vms["fixture-host"] = []*models.VM{{UUID: "fixture-uuid", Name: "fixture-vm", HostID: "fixture-host", Autostart: true, State: models.VMStopped}}
}
func TestRemainingAlarmPredicatesAndDuration(t *testing.T) {
	s := NewStoreWithBackend(nil, t.TempDir()+"/state.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	seedAlarmMetricInventory(s, now)
	as := remainingAlarmSet(t)
	for _, a := range as {
		a.ForSeconds = 2
	}
	if err := s.SetResourceAlarms(as); err != nil {
		t.Fatal(err)
	}
	if len(s.evaluateResourceAlarms(now)) != 0 || len(s.evaluateResourceAlarms(now.Add(time.Second))) != 0 {
		t.Fatal("hold bypassed")
	}
	events := s.evaluateResourceAlarms(now.Add(2 * time.Second))
	if len(events) != 4 {
		t.Fatal("four predicates did not fire", events)
	}
	if len(s.evaluateResourceAlarms(now.Add(3*time.Second))) != 0 {
		t.Fatal("episode duplicated")
	}
	s.mu.Lock()
	h := s.hosts["fixture-host"]
	h.LastSeen = now.Add(4 * time.Second)
	h.AgentVersion = " v" + strings.TrimPrefix(strings.TrimSpace(Version), "v") + " "
	s.storage[h.ID][0].AvailableGB = 25
	s.vms[h.ID][0].State = "RUNNING"
	s.mu.Unlock()
	events = s.evaluateResourceAlarms(now.Add(4 * time.Second))
	if len(events) != 4 {
		t.Fatal("clear predicates failed", events)
	}
	for _, e := range events {
		if e.Type != "alarm.cleared" {
			t.Fatal("unexpected event")
		}
	}
	// Version comparison is string inequality, not semantic ordering.
	s.mu.Lock()
	h.AgentVersion = "v999999.0.0"
	h.LastSeen = time.Time{}
	s.mu.Unlock()
	s.evaluateResourceAlarms(now.Add(10 * time.Second))
	events = s.evaluateResourceAlarms(now.Add(12 * time.Second))
	if len(events) != 1 || events[0].Detail["metric"] != alarmAgentVersion {
		t.Fatal("version/zero LastSeen semantics")
	}
	s.mu.Lock()
	h.LastSeen = now.Add(24 * time.Hour)
	s.mu.Unlock()
	if len(s.evaluateResourceAlarms(now.Add(13*time.Second))) != 0 {
		t.Fatal("future LastSeen breached")
	}
}
func TestPoolVMAlarmPausedTargetsAndScope(t *testing.T) {
	s := NewStoreWithBackend(nil, t.TempDir()+"/state.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	seedAlarmMetricInventory(s, now)
	as := remainingAlarmSet(t)[1:3]
	as[1].VMs = []string{"FIXTURE-UUID"}
	if err := s.SetResourceAlarms(as); err != nil {
		t.Fatal(err)
	}
	if len(s.evaluateResourceAlarms(now)) != 2 {
		t.Fatal("VM UUID scope or ignored host scope")
	}
	s.mu.Lock()
	s.hosts["fixture-host"].LastSeen = now.Add(-121 * time.Second)
	s.vms["fixture-host"][0].State = models.VMRunning
	s.storage["fixture-host"][0].AvailableGB = 100
	s.mu.Unlock()
	if len(s.evaluateResourceAlarms(now.Add(time.Second))) != 0 {
		t.Fatal("stale stored-online host evaluated")
	}
	for _, a := range s.GetResourceAlarms() {
		if len(a.FiringOn) != 1 {
			t.Fatal("offline episode discarded")
		}
	}
	s.mu.Lock()
	s.hosts["fixture-host"].LastSeen = now
	s.storage["fixture-host"][0].CapacityGB = 0
	s.mu.Unlock()
	if got := s.evaluateResourceAlarms(now.Add(2 * time.Second)); len(got) != 1 || got[0].Detail["metric"] != alarmVMStopped {
		t.Fatal("capacity skip/VM clear")
	}
	s.mu.Lock()
	s.storage["fixture-host"][0].CapacityGB = 100
	s.storage["fixture-host"][0].AvailableGB = 24.99
	s.mu.Unlock()
	if len(s.evaluateResourceAlarms(now.Add(3*time.Second))) != 0 {
		t.Fatal("pool cleared above margin")
	}
	s.mu.Lock()
	s.storage["fixture-host"][0].AvailableGB = 25
	s.mu.Unlock()
	if len(s.evaluateResourceAlarms(now.Add(4*time.Second))) != 1 {
		t.Fatal("pool failed exact boundary")
	}
	s.mu.Lock()
	s.vms["fixture-host"][0].Autostart = false
	s.vms["fixture-host"][0].State = models.VMStopped
	s.mu.Unlock()
	if len(s.evaluateResourceAlarms(now.Add(5*time.Second))) != 0 {
		t.Fatal("non-autostart VM fired")
	}
	s.mu.Lock()
	delete(s.hosts, "fixture-host")
	s.mu.Unlock()
	s.evaluateResourceAlarms(now.Add(6 * time.Second))
	if len(s.alarmStates) != 0 {
		t.Fatal("removed targets retained")
	}
}
func TestAlarmInventoryAmbiguityRefusedBeforeMutation(t *testing.T) {
	s := NewStoreWithBackend(nil, t.TempDir()+"/state.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	now := time.Now()
	seedAlarmMetricInventory(s, now)
	as := remainingAlarmSet(t)
	if err := s.SetResourceAlarms(as); err != nil {
		t.Fatal(err)
	}
	s.evaluateResourceAlarms(now)
	before := s.GetResourceAlarms()
	p := &models.TelemetryPayload{HostID: "other", VMs: []models.VM{{UUID: "second", Name: "FIXTURE-VM", State: models.VMStopped, Autostart: true}}}
	if s.ApplyTelemetry(p) == nil || s.hosts["other"] != nil || !reflect.DeepEqual(before, s.GetResourceAlarms()) {
		t.Fatal("ambiguous telemetry mutated inventory or episodes")
	}
	s.mu.Lock()
	s.vms["fixture-host"][0].State = "future-state"
	s.mu.Unlock()
	if s.resourceAlarmEvaluationError() == "" || len(s.evaluateResourceAlarms(now)) != 0 {
		t.Fatal("unknown dynamic state silently evaluated")
	}
	if _, err := s.ExportSnapshot(); err == nil {
		t.Fatal("unsupported inventory exported")
	}
	var original persistData
	raw := []byte(`{"alarms":[{"id":"vm","name":"vm","enabled":true,"metric":"vm.unexpectedly_stopped","threshold":0,"severity":"warning"}],"hosts":{"a":{"id":"a"},"b":{"id":"b"}},"vms":{"a":[{"host_id":"a","name":"same","uuid":"1"}],"b":[{"host_id":"b","name":"same","uuid":"2"}]}}`)
	if json.Unmarshal(raw, &original) == nil {
		t.Fatal("ambiguous snapshot accepted")
	}
	pool := remainingAlarmSet(t)[1]
	pool.Hosts = nil
	d := &persistData{Alarms: []*models.Alarm{pool}, Hosts: map[string]*models.Host{"a": {ID: "a"}, "a/b": {ID: "a/b"}}, Storage: map[string][]*models.StoragePool{"a": {{HostID: "a", Name: "b/c", CapacityGB: 100}}, "a/b": {{HostID: "a/b", Name: "c", CapacityGB: 100}}}}
	if validateAlarmInventory(d) == nil {
		t.Fatal("colliding pool display identities accepted")
	}
}
func TestRemainingAlarmThresholdAdmission(t *testing.T) {
	for _, a := range remainingAlarmSet(t) {
		if err := validateAlarm(a); err != nil {
			t.Fatal(err)
		}
		a.Threshold = -1
		if validateAlarm(a) == nil {
			t.Fatal("invalid metric threshold accepted")
		}
	}
	a := remainingAlarmSet(t)[0]
	a.Threshold = 59.999
	if validateAlarm(a) == nil {
		t.Fatal("sub-minute silence admitted")
	}
}

func TestAlarmSilenceExactBoundaryAndEmptyVersion(t *testing.T) {
	s := NewStoreWithBackend(nil, t.TempDir()+"/state.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	now := time.Now()
	seedAlarmMetricInventory(s, now)
	as := remainingAlarmSet(t)
	if err := s.SetResourceAlarms([]*models.Alarm{as[0], as[3]}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.hosts["fixture-host"].LastSeen = now.Add(-59999 * time.Millisecond)
	s.hosts["fixture-host"].AgentVersion = ""
	s.mu.Unlock()
	if len(s.evaluateResourceAlarms(now)) != 0 {
		t.Fatal("silence triggered early or empty version fired")
	}
	s.mu.Lock()
	s.hosts["fixture-host"].LastSeen = now.Add(-60 * time.Second)
	s.mu.Unlock()
	if got := s.evaluateResourceAlarms(now); len(got) != 1 || got[0].Type != "alarm.fired" {
		t.Fatal("silence equality did not fire")
	}
	s.mu.Lock()
	s.hosts["fixture-host"].LastSeen = now.Add(-59 * time.Second)
	s.mu.Unlock()
	if got := s.evaluateResourceAlarms(now); len(got) != 1 || got[0].Type != "alarm.cleared" {
		t.Fatal("silence incorrectly used hysteresis")
	}
}
