//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryAcceptanceResourceAlarmLifecycle(t *testing.T) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("current artifact required")
	}
	b := newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	root := t.TempDir()
	s, token := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	s.mu.Lock()
	s.hosts["fixture-host"] = &models.Host{ID: "fixture-host", CPUUsage: 10, MemUsage: 10}
	s.hosts["excluded"] = &models.Host{ID: "excluded", CPUUsage: 99, MemUsage: 99}
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	original := alarmFixture(t, "original-cpu")
	originalPath := filepath.Join(root, "original-alarm.bson")
	if err := os.WriteFile(originalPath, original, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Collection(mcoAlarms).InsertOne(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	p := startAcceptance(t, binary, root, b, token, false)
	read := func() []*models.Alarm {
		t.Helper()
		var response struct {
			Alarms []*models.Alarm `json:"alarms"`
		}
		if err := json.Unmarshal(p.request(t, "GET", "/api/alarms", nil, 200), &response); err != nil {
			t.Fatal(err)
		}
		return response.Alarms
	}
	put := func(in []*models.Alarm) []*models.Alarm {
		t.Helper()
		raw, err := json.Marshal(cloneAlarms(in))
		if err != nil {
			t.Fatal(err)
		}
		p.request(t, "PUT", "/api/alarms", raw, 200)
		return read()
	}
	metricBytes := p.request(t, "GET", "/api/alarms/metrics", nil, 200)
	if !bytes.Contains(metricBytes, []byte(alarmHostCPU)) || !bytes.Contains(metricBytes, []byte(alarmHostMemory)) || bytes.Contains(metricBytes, []byte("future.unknown_metric")) {
		t.Fatal("supported metric list wrong")
	}
	config := cloneAlarms(read())
	if len(config) != 1 {
		t.Fatal("original not read")
	}
	memory := *config[0]
	memory.ID = ""
	memory.Name = "Synthetic memory alarm"
	memory.Metric = alarmHostMemory
	memory.ForSeconds = 1
	config = put(append(config, &memory))
	memoryID := ""
	for _, a := range config {
		if a.Metric == alarmHostMemory {
			memoryID = a.ID
			if a.CreatedAt == "" || a.UpdatedAt == "" {
				t.Fatal("new metadata absent")
			}
		}
	}
	if memoryID == "" {
		t.Fatal("new ID absent")
	}
	// Unsupported disabled definitions and explicit null must not alter the set.
	bad := cloneAlarms(config)
	bad[0].Metric = "future.unknown_metric"
	bad[0].Enabled = false
	raw, _ := json.Marshal(bad)
	p.request(t, "PUT", "/api/alarms", raw, 400)
	raw, _ = json.Marshal(cloneAlarms(config))
	nullRaw := bytes.Replace(raw, []byte(`"for_seconds":0`), []byte(`"for_seconds":null`), 1)
	if bytes.Equal(raw, nullRaw) {
		t.Fatal("null test did not mutate input")
	}
	p.request(t, "PUT", "/api/alarms", nullRaw, 400)
	telemetry := func(value float64) {
		t.Helper()
		raw, _ := json.Marshal(models.TelemetryPayload{HostID: "fixture-host", CPUUsage: value, MemUsage: value, Timestamp: time.Now()})
		p.request(t, "POST", "/api/telemetry", raw, 200)
	}
	status := func(id string) bool {
		t.Helper()
		for _, a := range read() {
			if a.ID == id {
				if len(a.FiringOn) > 1 || len(a.FiringOn) == 1 && a.FiringOn[0] != "fixture-host" {
					t.Fatal("host scope or ignored VM scope changed")
				}
				return len(a.FiringOn) == 1
			}
		}
		t.Fatal("alarm missing")
		return false
	}
	waitStatus := func(id string, want bool) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			if status(id) == want {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatal("alarm state deadline")
	}
	telemetry(90)
	waitStatus("original-cpu", true)
	if status(memoryID) {
		t.Fatal("memory fired without duration")
	}
	waitStatus(memoryID, true)
	telemetry(78)
	time.Sleep(resourceAlarmInterval + time.Second)
	if !status("original-cpu") || !status(memoryID) {
		t.Fatal("hysteresis cleared above margin")
	}
	telemetry(75)
	waitStatus("original-cpu", false)
	waitStatus(memoryID, false)
	config = cloneAlarms(read())
	for _, a := range config {
		if a.ID == "original-cpu" {
			a.Name = "Edited synthetic CPU"
			a.Threshold = 85
		} else {
			a.Enabled = false
		}
	}
	config = put(config)
	telemetry(90)
	waitStatus("original-cpu", true)
	if status(memoryID) {
		t.Fatal("disabled memory alarm fired")
	}
	// Delete the memory definition and prove later restore cannot resurrect it.
	kept := []*models.Alarm{}
	for _, a := range config {
		if a.ID == "original-cpu" {
			kept = append(kept, a)
		}
	}
	put(kept)
	verify := func() {
		t.Helper()
		var got bson.Raw
		if err := b.db.Collection(mcoAlarms).FindOne(t.Context(), bson.D{{Key: "_id", Value: "original-cpu"}}).Decode(&got); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"id", "metric", "enabled", "forseconds", "severity", "hosts", "vms", "createdat", "opaque"} {
			if !equalRaw(got.Lookup(key), original.Lookup(key)) {
				t.Fatal("alarm preservation changed", key)
			}
		}
		if got.Lookup("threshold").Double() != 85 || got.Lookup("name").StringValue() != "Edited synthetic CPU" {
			t.Fatal("known edit missing")
		}
		n, err := b.db.Collection(mcoAlarms).CountDocuments(t.Context(), bson.D{{Key: "_id", Value: memoryID}})
		if err != nil || n != 0 {
			t.Fatal("deleted alarm resurrected")
		}
		disk, err := os.ReadFile(originalPath)
		if err != nil || !bytes.Equal(disk, original) {
			t.Fatal("original changed")
		}
	}
	p.stop(t, false)
	verify()
	p = startAcceptance(t, binary, root, b, token, false)
	if status("original-cpu") {
		t.Fatal("firing state persisted across restart")
	}
	waitStatus("original-cpu", true)
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restoreRoot := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restoreRoot, 0)
	if snapshot.EntityOverlay.Version != 8 || len(snapshot.Alarms) != 1 || len(snapshot.Alarms[0].FiringOn) != 0 {
		t.Fatal("backup config/state incorrect")
	}
	b = newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	if status("original-cpu") {
		t.Fatal("restore resurrected firing state")
	}
	waitStatus("original-cpu", true)
	verify()
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	if !equalRaw(bson.Raw(second.EntityOverlay.Collections[mcoAlarms]["original-cpu"]).Lookup("opaque"), original.Lookup("opaque")) {
		t.Fatal("second backup lost opaque")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	if len(read()) != 1 || status("original-cpu") {
		t.Fatal("second restart config/transient state wrong")
	}
	verify()
	p.stop(t, false)
	sum := sha256.Sum256(original)
	emitAcceptance(t, "resource_alarm_lifecycle", map[string]any{"backend": "mongo", "fixture_schema": "synthetic-resource-alarms-bson-v1", "original_sha256": hex.EncodeToString(sum[:]), "cpu_memory_scope_duration_hysteresis": true, "edit_disable_delete_restart": true, "transient_state_reconstructed": true, "application_backup_fresh_restore": true, "second_backup_restart": true, "opaque_preserved": true, "originals_unchanged": true, "ttl_monitor_disabled": true, "remote_delivery_verified": false, "other_alarm_metrics_supported": true, "full_legacy_compatible": false})
}
