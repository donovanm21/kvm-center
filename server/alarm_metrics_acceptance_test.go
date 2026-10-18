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
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHistoryAcceptanceRemainingAlarmMetrics(t *testing.T) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("current artifact required")
	}
	b := newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	root := t.TempDir()
	_, token := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	originals := map[string]bson.Raw{}
	checksums := map[string]string{}
	for _, a := range remainingAlarmSet(t) {
		raw := remainingAlarmFixture(t, a.ID, a.Metric)
		originals[a.ID] = raw
		sum := sha256.Sum256(raw)
		checksums[a.ID] = hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(root, a.ID+".bson"), raw, 0400); err != nil {
			t.Fatal(err)
		}
		if _, err := b.db.Collection(mcoAlarms).InsertOne(t.Context(), raw); err != nil {
			t.Fatal(err)
		}
	}
	p := startAcceptance(t, binary, root, b, token, false)
	read := func() []*models.Alarm {
		t.Helper()
		var response struct {
			Alarms          []*models.Alarm `json:"alarms"`
			EvaluationError string          `json:"evaluation_error"`
		}
		if err := json.Unmarshal(p.request(t, "GET", "/api/alarms", nil, 200), &response); err != nil || response.EvaluationError != "" {
			t.Fatal("alarm read or evaluation refused", err)
		}
		return response.Alarms
	}
	var version struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(p.request(t, "GET", "/api/version", nil, 200), &version); err != nil || version.Version == "" {
		t.Fatal("version unavailable")
	}
	var metrics []map[string]any
	if err := json.Unmarshal(p.request(t, "GET", "/api/alarms/metrics", nil, 200), &metrics); err != nil || len(metrics) != 6 {
		t.Fatal("six metrics unavailable")
	}
	telemetry := func(breached bool) {
		t.Helper()
		stamp := time.Now()
		agent := " v" + strings.TrimPrefix(strings.TrimSpace(version.Version), "v") + " "
		available := float64(25)
		state := models.VMRunning
		if breached {
			stamp = stamp.Add(-61 * time.Second)
			agent = "synthetic-different-version"
			available = 10
			state = models.VMStopped
		}
		payload := models.TelemetryPayload{HostID: "fixture-host", Timestamp: stamp, AgentVersion: agent, VMs: []models.VM{{UUID: "fixture-uuid", Name: "fixture-vm", Autostart: true, State: state}}, Storage: []models.StoragePool{{Name: "pool", CapacityGB: 100, AvailableGB: available}}}
		raw, _ := json.Marshal(payload)
		p.request(t, "POST", "/api/telemetry", raw, 200)
	}
	assertStatus := func(want bool) bool {
		t.Helper()
		as := read()
		if len(as) != 4 {
			t.Fatal("definitions lost")
		}
		all := true
		for _, a := range as {
			target := "fixture-host"
			if a.Metric == alarmVMStopped {
				target = "fixture-vm"
			}
			if a.Metric == alarmPoolUsed {
				target += "/pool"
			}
			if len(a.FiringOn) > 1 || len(a.FiringOn) == 1 && a.FiringOn[0] != target {
				t.Fatal("legacy target identity changed")
			}
			if (len(a.FiringOn) == 1) != want {
				all = false
			}
		}
		return all
	}
	waitStatus := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			if assertStatus(want) {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatal("remaining alarm state deadline")
	}
	telemetry(true)
	waitStatus(true)
	// Both ambiguous live telemetry and invalid metric parameters refuse atomically.
	before := read()
	bad := models.TelemetryPayload{HostID: "other", Timestamp: time.Now(), VMs: []models.VM{{UUID: "other", Name: "FIXTURE-VM", State: models.VMStopped, Autostart: true}}}
	raw, _ := json.Marshal(bad)
	p.request(t, "POST", "/api/telemetry", raw, 409)
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("refused telemetry changed alarms")
	}
	invalid := cloneAlarms(before)
	for _, a := range invalid {
		if a.Metric == alarmAgentVersion {
			a.Threshold = 1
		}
	}
	raw, _ = json.Marshal(invalid)
	p.request(t, "PUT", "/api/alarms", raw, 400)
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("refused config changed alarms")
	}
	telemetry(false)
	waitStatus(false)
	edited := cloneAlarms(read())
	for _, a := range edited {
		a.Name += " edited"
	}
	raw, _ = json.Marshal(edited)
	p.request(t, "PUT", "/api/alarms", raw, 200)
	verify := func() {
		t.Helper()
		for id, original := range originals {
			var got bson.Raw
			if err := b.db.Collection(mcoAlarms).FindOne(t.Context(), bson.D{{Key: "_id", Value: id}}).Decode(&got); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"id", "metric", "enabled", "threshold", "forseconds", "severity", "hosts", "vms", "createdat", "opaque"} {
				if !equalRaw(original.Lookup(key), got.Lookup(key)) {
					t.Fatal("remaining alarm BSON changed", key)
				}
			}
			if !strings.HasSuffix(got.Lookup("name").StringValue(), " edited") {
				t.Fatal("known edit missing")
			}
			disk, err := os.ReadFile(filepath.Join(root, id+".bson"))
			if err != nil || !bytes.Equal(original, disk) {
				t.Fatal("original changed")
			}
		}
	}
	p.stop(t, false)
	verify()
	p = startAcceptance(t, binary, root, b, token, false)
	if !assertStatus(false) {
		t.Fatal("firing state persisted")
	}
	telemetry(true)
	waitStatus(true)
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restoreRoot := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restoreRoot, 0)
	if snapshot.EntityOverlay.Version != 8 || len(snapshot.Alarms) != 4 {
		t.Fatal("remaining alarm backup missing")
	}
	b = newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	if !assertStatus(false) {
		t.Fatal("restore resurrected transient firing state")
	}
	verify()
	telemetry(true)
	waitStatus(true)
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	for id, original := range originals {
		if !equalRaw(bson.Raw(second.EntityOverlay.Collections[mcoAlarms][id]).Lookup("opaque"), original.Lookup("opaque")) {
			t.Fatal("second backup lost opaque")
		}
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restoreRoot, b, token, false)
	if !assertStatus(false) {
		t.Fatal("second restart retained firing state")
	}
	verify()
	p.stop(t, false)
	emitAcceptance(t, "remaining_alarm_metrics_lifecycle", map[string]any{"backend": "mongo", "fixture_schema": "synthetic-remaining-alarms-bson-v1", "original_checksums": checksums, "metrics": []string{alarmHostOffline, alarmPoolUsed, alarmVMStopped, alarmAgentVersion}, "native_worker_fire_clear": true, "ambiguous_telemetry_refused_before_mutation": true, "known_edits_restart": true, "application_backup_fresh_restore": true, "second_backup_restart": true, "opaque_preserved": true, "originals_unchanged": true, "ttl_monitor_disabled": true, "transient_state_reconstructed": true, "remote_delivery_verified": false, "vm_stop_intent_inferred": false, "concurrent_external_writer_supported": false, "full_legacy_compatible": false})
}
