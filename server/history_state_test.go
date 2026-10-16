package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
)

func historyFixture() *historyState {
	at := time.Now().Unix()
	start := at - at%300
	return &historyState{Version: 1, Hosts: map[string]hostHistory{"fixture": {Last: at, Points: []historyBucket{{T: start, N: 1, CPU: 125000, CPUMax: 125000, Mem: 250000, MemMax: 250000, Storage: 500000, StorageMax: 500000, VMs: 2}}}}}
}
func TestHistoryBucketWeightsOrderingAndIsolation(t *testing.T) {
	now := time.Now().UTC().Truncate(5 * time.Minute)
	s := &Store{historyEnabled: true}
	other := &Store{historyEnabled: true}
	if !s.recordHistory("fixture", now, 10, 20, 30, 2) || !s.recordHistory("fixture", now.Add(time.Second), 30, 40, 50, 3) {
		t.Fatal("valid samples rejected")
	}
	for _, at := range []time.Time{now, now.Add(time.Second), now.Add(-time.Second)} {
		if s.recordHistory("fixture", at, 99, 99, 99, 9) {
			t.Fatal("duplicate/reordered observation accepted")
		}
	}
	h := s.history.Hosts["fixture"]
	p := h.Points[0]
	if p.N != 2 || p.CPU != 400000 || p.CPUMax != 300000 || p.VMs != 3 {
		t.Fatalf("bad weighted bucket: %+v", p)
	}
	if other.history != nil {
		t.Fatal("cross-Store history leak")
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), -1, 101} {
		if s.recordHistory("fixture", now.Add(2*time.Second), value, 0, 0, 0) {
			t.Fatal("invalid percent accepted")
		}
	}
	s.historyEnabled = false
	if s.recordHistory("new", now, 0, 0, 0, 0) || len(s.history.Hosts) != 1 {
		t.Fatal("disabled recorder changed state")
	}
}
func TestHistoryRetentionCapacityAndEncodedBounds(t *testing.T) {
	now := time.Now().Unix()
	cut := historyCutoff(now)
	d := &historyState{Version: 1, Hosts: map[string]hostHistory{}}
	for i := 0; i < historyMaxHosts; i++ {
		h := hostHistory{Last: cut + (historyMaxPoints-1)*300 + 299}
		for j := 0; j < historyMaxPoints; j++ {
			h.Points = append(h.Points, historyBucket{T: cut + int64(j)*300, N: 300, CPU: 300000000, CPUMax: 1000000, Mem: 300000000, MemMax: 1000000, Storage: 300000000, StorageMax: 1000000, VMs: 1000000})
		}
		d.Hosts[fmt.Sprintf("%03d%s", i, strings.Repeat("x", 252))] = h
	}
	if err := d.validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := bson.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > historyMaxBytes || len(encoded) > historyMaxBytes {
		t.Fatal("count bounds exceed encoded ceiling")
	}
	t.Logf("maximum fixture JSON=%d BSON=%d", len(raw), len(encoded))
	pretty, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var back historyState
	if err := json.Unmarshal(pretty, &back); err != nil {
		t.Fatal(err)
	}
	s := &Store{historyEnabled: true, history: d}
	if s.recordHistory("over-cap", time.Unix(now, 0), 0, 0, 0, 0) {
		t.Fatal("host cap exceeded")
	}
	old := historyFixture()
	for id, h := range old.Hosts {
		h.Last = cut - 1
		h.Points[0].T = cut - 300
		old.Hosts[id] = h
	}
	s.history = old
	if !s.recordHistory("replacement", time.Unix(now, 0), 1, 1, 1, 1) || len(s.history.Hosts) != 1 {
		t.Fatal("expired host blocked admission")
	}
	pruneHistory(d, now+historyMaxPoints*300)
	if len(d.Hosts) != 0 {
		t.Fatal("wall-clock expiry retained old buckets")
	}
}
func TestHistoryStrictFormatsAndLoadGate(t *testing.T) {
	good, _ := json.Marshal(historyFixture())
	for _, payload := range []string{`null`, `{}`, `{"version":99,"hosts":{}}`, `{"version":1,"Version":1,"hosts":{}}`, `{"version":1,"hosts":{},"future":1}`, `{"version":1,"version":1,"hosts":{}}`, strings.Replace(string(good), `"vms":2`, `"vms":null`, 1), strings.Replace(string(good), `"n":1`, `"n":0`, 1), strings.Replace(string(good), `"cpu_sum":125000`, `"cpu_sum":999999999`, 1), strings.Replace(string(good), `"points":[{`, `"points":[{"future":true,`, 1)} {
		var d persistData
		if json.Unmarshal([]byte(`{"host_history_state":`+payload+`}`), &d) == nil {
			t.Fatalf("invalid history accepted: %s", payload)
		}
	}
	for _, raw := range []string{`{"history":[]}`, `{"HISTORY":[]}`, `{"HOST_HISTORY_STATE":null}`, `{"host_history_state":{"version":1,"hosts":{}},"host_history_state":null}`} {
		var d persistData
		if json.Unmarshal([]byte(raw), &d) == nil {
			t.Fatal("ambiguous namespace accepted")
		}
	}
	for _, doc := range []bson.D{{{Key: "version", Value: 1}, {Key: "version", Value: 1}, {Key: "hosts", Value: bson.M{}}}, {{Key: "version", Value: 1}, {Key: "hosts", Value: bson.M{}}, {Key: "future", Value: true}}} {
		raw, _ := bson.Marshal(doc)
		var d historyState
		if bson.Unmarshal(raw, &d) == nil {
			t.Fatal("unsupported BSON accepted")
		}
	}
	invalid := historyFixture()
	h := invalid.Hosts["fixture"]
	h.Last = h.Points[0].T
	h.Points[0].N = 300
	invalid.Hosts["fixture"] = h
	if invalid.validate() == nil {
		t.Fatal("impossible same-second count accepted")
	}
	wire, _ := bson.Marshal(historyFixture())
	var decoded historyState
	if err := bson.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	raw := []byte(`{"host_history_state":{"version":99,"hosts":{}}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	s.saveWG.Wait()
	if s.SaveChecked() == nil {
		t.Fatal("unsupported load opened save gate")
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != string(raw) {
		t.Fatal("unsupported state overwritten")
	}
}
func TestHistoryPersistenceDirtyBackupClear(t *testing.T) {
	s, b := persistenceTestStore(t)
	s.history = historyFixture()
	raw, err := s.ExportSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	features, err := snapshotFeatureSchemas(raw)
	if err != nil || features["host_history_state"] != 1 {
		t.Fatal("backup metadata missing")
	}
	b.failNext = true
	if s.SaveChecked() == nil {
		t.Fatal("failure hidden")
	}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if !isDirty(b.attempts[0], mcoHostHistory) || !isDirty(b.attempts[1], mcoHostHistory) || isDirty(b.attempts[2], mcoHostHistory) {
		t.Fatal("dirty failure/retry/clean behavior broken")
	}
	path := filepath.Join(t.TempDir(), "history.json")
	dst := NewJSONBackend(path)
	var snapshot persistData
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := dst.Save(context.Background(), &snapshot); err != nil {
		t.Fatal(err)
	}
	loaded := NewStore(path)
	loaded.saveWG.Wait()
	t.Cleanup(func() { loaded.saveWG.Wait() })
	if loaded.historyEnabled || !reflect.DeepEqual(loaded.history, s.history) {
		t.Fatal("disabled reload lost history")
	}
	if err := loaded.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	latest, _ := dst.Load(context.Background())
	if !reflect.DeepEqual(latest.HostHistory, s.history) {
		t.Fatal("disabled save lost history")
	}
	loaded.mu.Lock()
	pruneHistory(loaded.history, time.Now().Unix()+historyMaxPoints*300)
	loaded.mu.Unlock()
	if err := loaded.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	latest, _ = dst.Load(context.Background())
	if latest.HostHistory != nil {
		t.Fatal("expiry clear resurrected")
	}
	s.history = nil
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if !isDirty(b.attempts[3], mcoHostHistory) || b.attempts[3].HostHistory != nil {
		t.Fatal("dirty absence not saved")
	}
}
func TestHistoryAPIObservationAndClockRollback(t *testing.T) {
	s := newTestStore(t)
	s.mu.Lock()
	s.history = historyFixture()
	s.hosts["fixture"] = &models.Host{ID: "fixture"}
	s.mu.Unlock()
	before, _ := s.ExportSnapshot()
	w := httptest.NewRecorder()
	handleHostHistory(s).ServeHTTP(w, httptest.NewRequest("GET", "/api/hosts/fixture/history?range=1h", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cpu_avg":12.5`) || !strings.Contains(w.Body.String(), `"collection_enabled":false`) {
		t.Fatal(w.Body)
	}
	after, _ := s.ExportSnapshot()
	if string(before) != string(after) {
		t.Fatal("read changed state")
	}
	for path, want := range map[string]int{"/api/hosts/missing/history": 404, "/api/hosts/fixture/history?range=1y": 400} {
		w = httptest.NewRecorder()
		handleHostHistory(s).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatal(w.Code)
		}
	}
	w = httptest.NewRecorder()
	requirePerm(s, "host.view", handleHostHistory(s)).ServeHTTP(w, httptest.NewRequest("GET", "/api/hosts/fixture/history", nil))
	if w.Code != 401 {
		t.Fatal("unauthenticated read allowed")
	}
	s.mu.Lock()
	s.historyEnabled = true
	h := s.history.Hosts["fixture"]
	h.Last = time.Now().Unix() + 3600
	h.Points[0].T = h.Last - h.Last%300
	s.history.Hosts["fixture"] = h
	s.mu.Unlock()
	if s.recordHistory("fixture", time.Now(), 0, 0, 0, 0) {
		t.Fatal("clock rollback accepted reordered sample")
	}
	w = httptest.NewRecorder()
	handleHostHistory(s).ServeHTTP(w, httptest.NewRequest("GET", "/api/hosts/fixture/history", nil))
	if !strings.Contains(w.Body.String(), `"clock_ahead":true`) || !strings.Contains(w.Body.String(), `"points":[]`) {
		t.Fatal(w.Body)
	}
}
func TestHistoryTelemetryUsesServerTimeAndTrackedDrain(t *testing.T) {
	s := newTestStore(t)
	s.mu.Lock()
	s.historyEnabled = true
	s.mu.Unlock()
	start := time.Now().Unix()
	s.ApplyTelemetry(&models.TelemetryPayload{HostID: "fixture", Timestamp: time.Unix(1, 0), CPUUsage: 10, MemUsage: 20})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.work.seal(ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	h := s.history.Hosts["fixture"]
	s.mu.RUnlock()
	if h.Last < start || len(h.Points) != 1 {
		t.Fatal("history used agent time or escaped tracked work")
	}
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryNullScalarsAndDowngrade(t *testing.T) {
	fixture := historyFixture()
	raw, _ := json.Marshal(fixture)
	for _, key := range []string{"cpu_sum", "cpu_max", "mem_sum", "mem_max", "storage_sum", "storage_max", "vms"} {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		host := doc["hosts"].(map[string]any)["fixture"].(map[string]any)
		point := host["points"].([]any)[0].(map[string]any)
		point[key] = nil
		bad, _ := json.Marshal(doc)
		var state historyState
		if json.Unmarshal(bad, &state) == nil {
			t.Fatalf("JSON null %s accepted", key)
		}
		badBSON, err := bson.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if bson.Unmarshal(badBSON, &state) == nil {
			t.Fatalf("BSON null %s accepted", key)
		}
	}
	snapshot, _ := json.Marshal(persistData{HostHistory: fixture})
	var old struct {
		Hosts map[string]*models.Host `json:"hosts,omitempty"`
	}
	if err := json.Unmarshal(snapshot, &old); err != nil {
		t.Fatal(err)
	}
	rewritten, _ := json.Marshal(old)
	if strings.Contains(string(rewritten), "host_history_state") {
		t.Fatal("representative old codec unexpectedly preserved unknown history")
	}
	back := cloneHistory(fixture)
	h := back.Hosts["fixture"]
	h.Points[0].VMs++
	back.Hosts["fixture"] = h
	if verifyBackendRoundTrip(context.Background(), &persistData{HostHistory: fixture}, &stubBackend{stored: &persistData{HostHistory: back}}) == nil {
		t.Fatal("migration counts hid altered history")
	}
}

type historyPartialBackend struct {
	stubBackend
	history *historyState
	fail    bool
}

func (b *historyPartialBackend) Save(_ context.Context, d *persistData) error {
	if isDirty(d, mcoHostHistory) {
		b.history = cloneHistory(d.HostHistory)
	}
	if b.fail {
		return fmt.Errorf("synthetic later failure")
	}
	return nil
}
func TestHistoryPartialSaveRetryAndExpiredLoad(t *testing.T) {
	b := &historyPartialBackend{}
	s := drainStore(b)
	original := historyFixture()
	s.history = cloneHistory(original)
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	s.history = nil
	b.fail = true
	if s.SaveChecked() == nil {
		t.Fatal("partial clear failure hidden")
	}
	s.history = cloneHistory(original)
	b.fail = false
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.history, original) {
		t.Fatal("failed partial clear survived retry")
	}
	expired := cloneHistory(original)
	for id, h := range expired.Hosts {
		h.Last -= historyMaxPoints * 300
		for i := range h.Points {
			h.Points[i].T -= historyMaxPoints * 300
		}
		expired.Hosts[id] = h
	}
	loaded := NewStoreWithBackend(&stubBackend{stored: &persistData{HostHistory: expired}}, filepath.Join(t.TempDir(), "unused.json"))
	loaded.saveWG.Wait()
	if loaded.historyStateLocked() != nil {
		t.Fatal("load did not expire old data")
	}
}
