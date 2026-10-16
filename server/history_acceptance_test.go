//go:build integration && historyacceptance && linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
)

// This suite is a separate, non-race Linux acceptance job. No target or URI
// comes from a deployment; Mongo uses the integration suite's loopback guard.
func acceptanceCap() *historyState {
	now := time.Now().Unix() - 2
	cut := historyCutoff(now)
	d := &historyState{Version: 1, Hosts: map[string]hostHistory{}}
	for i := 0; i < 64; i++ {
		h := hostHistory{Last: now}
		for j := 0; j < 576; j++ {
			at := cut + int64(j)*300
			n := uint32(300)
			if j == 575 {
				n = uint32(now - at + 1)
			}
			h.Points = append(h.Points, historyBucket{T: at, N: n, CPU: uint64(n) * 1000000, CPUMax: 1000000, Mem: uint64(n) * 1000000, MemMax: 1000000, Storage: uint64(n) * 1000000, StorageMax: 1000000, VMs: 1000000})
		}
		d.Hosts[fmt.Sprintf("%03d%s", i, strings.Repeat("x", 252))] = h
	}
	return d
}
func acceptanceSeed(t *testing.T, b Backend, path string, cap bool) (*Store, string) {
	t.Helper()
	s := NewStoreWithBackend(b, path)
	s.saveWG.Wait()
	s.mu.Lock()
	s.users["fixture-reader"] = &models.User{ID: "fixture-reader", Username: "fixture-reader", Enabled: true, RoleIDs: []string{"fixture-role"}}
	s.roles["fixture-role"] = &models.Role{ID: "fixture-role", Permissions: []string{"host.view", "user.manage"}}
	if cap {
		s.history = acceptanceCap()
		for id := range s.history.Hosts {
			s.hosts[id] = &models.Host{ID: id}
		}
	}
	s.mu.Unlock()
	token, _, err := s.CreateAPIToken("acceptance", "fixture-reader", "user", time.Time{}, []string{"host.view", "user.manage"})
	if err != nil {
		t.Fatal(err)
	}
	s.saveWG.Wait()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	return s, token
}
func emitAcceptance(t *testing.T, kind string, data any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"kind": kind, "data": data})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("HISTORY_EVIDENCE " + string(raw))
}
func procValue(pid int, key string) uint64 {
	raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == key+":" {
			n, _ := strconv.ParseUint(f[1], 10, 64)
			return n * 1024
		}
	}
	return 0
}
func countFD(pid int) int { es, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid)); return len(es) }
func TestHistoryAcceptanceCapCosts(t *testing.T) {
	for _, mode := range []string{"json", "mongo"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			var b Backend = NewJSONBackend(path)
			if mode == "mongo" {
				b = newMongoIntegrationBackend(t)
			}
			s, _ := acceptanceSeed(t, b, path, false)
			baseStart := time.Now()
			if err := s.SaveChecked(); err != nil {
				t.Fatal(err)
			}
			baseSave := time.Since(baseStart).Seconds()
			runtime.GC()
			var baseMem runtime.MemStats
			runtime.ReadMemStats(&baseMem)
			s.history = acceptanceCap()
			for id := range s.history.Hosts {
				s.hosts[id] = &models.Host{ID: id}
			}
			s.historyEnabled = true
			raw, _ := json.Marshal(s.history)
			wire, _ := bson.Marshal(s.history)
			if len(raw) > historyMaxBytes || len(wire) > historyMaxBytes {
				t.Fatal("encoded bound exceeded")
			}
			if s.recordHistory("host-65", time.Now(), 1, 1, 1, 0) {
				t.Fatal("host cap bypass")
			}
			var latencies []float64
			var heaps []uint64
			var rss uint64
			startFD, startGo := countFD(os.Getpid()), runtime.NumGoroutine()
			began := time.Now()
			for i := 0; i < 20; i++ {
				time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
				at := time.Now()
				for id := range s.history.Hosts {
					if !s.recordHistory(id, at, 50, 50, 50, i) {
						t.Fatal("existing capped host could not collect")
					}
				}
				for _, h := range s.history.Hosts {
					if len(h.Points) != 576 {
						t.Fatal("capacity workload dropped below576 buckets")
					}
				}
				beganSave := time.Now()
				if err := s.SaveChecked(); err != nil {
					t.Fatal(err)
				}
				latencies = append(latencies, time.Since(beganSave).Seconds())
				runtime.GC()
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				heaps = append(heaps, m.HeapAlloc)
				rss = max(rss, procValue(os.Getpid(), "VmHWM"))
			}
			sort.Float64s(latencies)
			if latencies[19] > 5 || heaps[19] > 256<<20 || rss > 768<<20 || heaps[19] > heaps[4]+32<<20 || countFD(os.Getpid()) > startFD+8 || runtime.NumGoroutine() > startGo+8 {
				t.Fatalf("capacity ceiling: save_max=%.3fs heap=%d rss=%d heap_growth=%d fd_delta=%d goroutine_delta=%d", latencies[19], heaps[19], rss, int64(heaps[19])-int64(heaps[4]), countFD(os.Getpid())-startFD, runtime.NumGoroutine()-startGo)
			}
			disk := map[string]any{}
			if mode == "json" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				disk["snapshot_bytes"] = info.Size()
				if info.Size() > 32<<20 {
					t.Fatal("snapshot size exceeded")
				}
			} else {
				db, err := b.(*MongoBackend).dial(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				var stats bson.M
				if err := db.RunCommand(context.Background(), bson.D{{Key: "collStats", Value: mcoHostHistory}}).Decode(&stats); err != nil {
					t.Fatal(err)
				}
				var build bson.M
				if err := db.RunCommand(context.Background(), bson.D{{Key: "buildInfo", Value: 1}}).Decode(&build); err != nil {
					t.Fatal(err)
				}
				disk["mongo_version"] = build["version"]
				disk["collection_bytes"] = stats["size"]
				disk["storage_bytes"] = stats["storageSize"]
			}
			emitAcceptance(t, "cap_costs", map[string]any{"backend": mode, "empty_save_seconds": baseSave, "empty_heap_after_gc_bytes": baseMem.HeapAlloc, "accepted_observations": 1280, "cycles": 20, "seconds": time.Since(began).Seconds(), "json_bytes": len(raw), "bson_bytes": len(wire), "save_p50_seconds": latencies[9], "save_p95_seconds": latencies[18], "save_max_seconds": latencies[19], "heap_after_gc_first": heaps[0], "heap_after_gc_last": heaps[19], "rss_process_high_water_bytes": rss, "fd_delta": countFD(os.Getpid()) - startFD, "goroutine_delta": runtime.NumGoroutine() - startGo, "disk": disk})
		})
	}
}

type acceptanceProcess struct {
	cmd        *exec.Cmd
	done       chan error
	client     *http.Client
	url, token string
	stopped    bool
}

func startAcceptance(t *testing.T, binary, root string, b Backend, token string, enabled bool) *acceptanceProcess {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	args := []string{"-no-tls", "-addr", addr, "-tls-addr", "127.0.0.1:0", "-grpc-addr", "", "-data", filepath.Join(root, "state.json"), "-ca-dir", filepath.Join(root, "ca"), "-tls-dir", filepath.Join(root, "tls"), "-graceful-shutdown"}
	if enabled {
		args = append(args, "-performance-history")
	}
	if m, ok := b.(*MongoBackend); ok {
		args = append(args, "-mongo-url", m.uri, "-mongo-db", m.dbName)
	} else {
		args = append(args, "-mongo-url", "")
	}
	p := &acceptanceProcess{cmd: exec.Command(binary, args...), done: make(chan error, 1), client: &http.Client{Timeout: 10 * time.Second}, url: "http://" + addr, token: token}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "KVM_CENTER_") && !strings.HasPrefix(v, "KVMC_") {
			p.cmd.Env = append(p.cmd.Env, v)
		}
	}
	log, err := os.Create(filepath.Join(root, "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	p.cmd.Stdout = log
	p.cmd.Stderr = log
	if err := p.cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	go func() { p.done <- p.cmd.Wait(); log.Close() }()
	t.Cleanup(func() { p.stop(t, true) })
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		r, err := p.client.Get(p.url + "/health")
		if err == nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			if r.StatusCode == 200 {
				return p
			}
		}
		select {
		case err := <-p.done:
			p.stopped = true
			t.Fatalf("server exited before readiness: %v", err)
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("server readiness timeout")
	return nil
}
func (p *acceptanceProcess) stop(t *testing.T, kill bool) {
	t.Helper()
	if p.stopped {
		return
	}
	if kill {
		p.cmd.Process.Kill()
	} else {
		p.cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case err := <-p.done:
		p.stopped = true
		if !kill && err != nil {
			t.Errorf("graceful process exit: %v", err)
		}
	case <-time.After(25 * time.Second):
		p.cmd.Process.Kill()
		<-p.done
		p.stopped = true
		t.Error("process stop exceeded25s")
	}
	p.client.CloseIdleConnections()
}
func (p *acceptanceProcess) request(t *testing.T, method, path string, body []byte, want int) []byte {
	t.Helper()
	r, err := http.NewRequest(method, p.url+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+p.token)
	r.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 40<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s returned%d want%d", method, path, response.StatusCode, want)
	}
	return raw
}
func (p *acceptanceProcess) history(t *testing.T, id string) hostHistoryResponse {
	t.Helper()
	var h hostHistoryResponse
	if err := json.Unmarshal(p.request(t, "GET", "/api/hosts/"+id+"/history", nil, 200), &h); err != nil {
		t.Fatal(err)
	}
	return h
}

type hostHistoryResponse struct {
	Enabled bool               `json:"collection_enabled"`
	Points  []historyViewPoint `json:"points"`
	Last    int64              `json:"last_sample"`
}

func (p *acceptanceProcess) sample(t *testing.T, id string, cpu float64) hostHistoryResponse {
	t.Helper()
	before := p.history(t, id).Last
	time.Sleep(time.Until(time.Unix(before+1, 0)))
	raw, _ := json.Marshal(models.TelemetryPayload{HostID: id, CPUUsage: cpu, MemUsage: 50, Timestamp: time.Unix(1, 0)})
	p.request(t, "POST", "/api/telemetry", raw, 200)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h := p.history(t, id)
		if h.Last > before {
			return h
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("accepted telemetry never reached history")
	return hostHistoryResponse{}
}
func copyAcceptance(t *testing.T, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func unpackAcceptance(t *testing.T, raw []byte, root string, schema int) *persistData {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg || h.Size > 32<<20 || (h.Name != "manifest.json" && h.Name != "store.json" && !strings.HasPrefix(h.Name, "ca/") && !strings.HasPrefix(h.Name, "tls/")) || strings.Contains(h.Name, "..") {
			t.Fatal("unexpected backup entry")
		}
		data, err := io.ReadAll(io.LimitReader(tr, 32<<20))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := entries[h.Name]; ok {
			t.Fatal("duplicate archive entry")
		}
		entries[h.Name] = data
	}
	var manifest backupManifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.FeatureSchemas["host_history_state"] != schema || len(manifest.Files) != len(entries)-1 {
		t.Fatal("backup manifest mismatch")
	}
	for name, sum := range manifest.Files {
		data, ok := entries[name]
		hash := sha256.Sum256(data)
		if !ok || hex.EncodeToString(hash[:]) != sum {
			t.Fatal("backup checksum mismatch")
		}
		path := filepath.Join(root, name)
		if name == "store.json" {
			path = filepath.Join(root, "state.json")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var d persistData
	if err := json.Unmarshal(entries["store.json"], &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func TestHistoryAcceptanceArtifactLifecycle(t *testing.T) {
	binary, old := os.Getenv("KVMC_HISTORY_BINARY"), os.Getenv("KVMC_HISTORY_OLD_BINARY")
	if binary == "" || old == "" || len(os.Getenv("KVMC_HISTORY_SHA")) != 40 {
		t.Fatal("exact current and pinned pre-history artifacts required")
	}
	for _, mode := range []string{"json", "mongo"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "state.json")
			var b Backend = NewJSONBackend(path)
			if mode == "mongo" {
				b = newMongoIntegrationBackend(t)
			}
			s, token := acceptanceSeed(t, b, path, false)
			s.mu.Lock()
			s.hosts["fixture"] = &models.Host{ID: "fixture"}
			s.mu.Unlock()
			if err := s.SaveChecked(); err != nil {
				t.Fatal(err)
			}
			preServer := startAcceptance(t, old, root, b, token, false)
			pre := preServer.request(t, "GET", "/api/admin/backup", nil, 200)
			preServer.stop(t, false)
			// Pre-upgrade backup is captured before any newer history writes. Never
			// point the old executable at the upgraded writer's state.
			baseline := startAcceptance(t, binary, root, b, token, true)
			for i := 0; i < 20; i++ {
				baseline.history(t, "fixture")
			}
			baseRSS := procValue(baseline.cmd.Process.Pid, "VmHWM")
			baseline.stop(t, false)
			s.mu.Lock()
			s.history = acceptanceCap()
			for id, h := range s.history.Hosts {
				delete(s.history.Hosts, id)
				s.history.Hosts["fixture"] = h
				break
			}
			for id := range s.history.Hosts {
				s.hosts[id] = &models.Host{ID: id}
			}
			s.mu.Unlock()
			if err := s.SaveChecked(); err != nil {
				t.Fatal(err)
			}
			p := startAcceptance(t, binary, root, b, token, true)
			var apiLatencies []float64
			for i := 0; i < 20; i++ {
				start := time.Now()
				p.history(t, "fixture")
				apiLatencies = append(apiLatencies, time.Since(start).Seconds())
			}
			sort.Float64s(apiLatencies)
			processRSS := procValue(p.cmd.Process.Pid, "VmHWM")
			if processRSS == 0 || processRSS > 768<<20 || apiLatencies[19] > 2 {
				t.Fatalf("artifact ceiling: rss=%d api_max=%.3fs", processRSS, apiLatencies[19])
			}
			p.request(t, "POST", "/api/telemetry", []byte(`{"host_id":"over-cap","cpu_usage":10}`), 200)
			if len(p.history(t, "over-cap").Points) != 0 {
				t.Fatal("artifact admitted host65")
			}
			emitAcceptance(t, "artifact_cap", map[string]any{"backend": mode, "empty_rss_high_water_bytes": baseRSS, "rss_high_water_bytes": processRSS, "api_p50_seconds": apiLatencies[9], "api_p95_seconds": apiLatencies[18], "api_max_seconds": apiLatencies[19]})
			if !p.history(t, "fixture").Enabled {
				t.Fatal("history flag not enabled")
			}
			health := p.request(t, "GET", "/api/admin/system-health", nil, 200)
			if !bytes.Contains(health, []byte(os.Getenv("KVMC_HISTORY_SHA"))) {
				t.Fatal("artifact SHA not exposed")
			}
			spa := p.request(t, "GET", "/", nil, 200)
			if !bytes.Contains(spa, []byte("/assets/")) {
				t.Fatal("production SPA missing")
			}
			observed := p.sample(t, "fixture", 25)
			p.stop(t, false)
			p = startAcceptance(t, binary, root, b, token, true)
			if !acceptancePointsEqual(observed.Points, p.history(t, "fixture").Points) {
				t.Fatal("graceful restart lost history")
			}
			// Independently loaded checkpoint, not telemetry/API acknowledgement.
			checkpoint, err := b.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if checkpoint.HostHistory == nil || len(checkpoint.HostHistory.Hosts) != 64 {
				t.Fatal("drained cap has incorrect host count")
			}
			if _, ok := checkpoint.HostHistory.Hosts["over-cap"]; ok {
				t.Fatal("host65 admitted after drain")
			}
			durableState := cloneHistory(checkpoint.HostHistory)
			durable := durableState.Hosts["fixture"]
			transient := p.sample(t, "fixture", 75)
			p.stop(t, true)
			killed, err := b.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			expectedTransient := &Store{historyEnabled: true, history: cloneHistory(durableState)}
			if !expectedTransient.recordHistory("fixture", time.Unix(transient.Last, 0), 75, 50, 0, 0) {
				t.Fatal("transient expectation invalid")
			}
			pruneHistory(durableState, time.Now().Unix())
			pruneHistory(expectedTransient.history, time.Now().Unix())
			pruneHistory(killed.HostHistory, time.Now().Unix())
			if !reflect.DeepEqual(killed.HostHistory, durableState) && !reflect.DeepEqual(killed.HostHistory, expectedTransient.history) {
				t.Fatal("SIGKILL changed full durable history outside accepted transient sample")
			}
			p = startAcceptance(t, binary, root, b, token, true)
			recovered := p.history(t, "fixture")
			for id, h := range killed.HostHistory.Hosts {
				if !acceptancePointsEqual(acceptanceView(h), p.history(t, id).Points) {
					t.Fatal("restarted API changed capped checkpoint")
				}
			}
			if recovered.Last < durable.Last || recovered.Last > transient.Last {
				t.Fatal("SIGKILL recovery violated durable checkpoint")
			}
			// No ingestion while capturing archive; stop writer before applying it.
			archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
			p.request(t, "POST", "/api/admin/restore", []byte("unused"), 503)
			expected := p.history(t, "fixture")
			p.stop(t, false)
			restoredRoot := t.TempDir()
			snapshot := unpackAcceptance(t, archive, restoredRoot, 1)
			var restoreBackend Backend = NewJSONBackend(filepath.Join(restoredRoot, "state.json"))
			if mode == "mongo" {
				restoreBackend = newMongoIntegrationBackend(t)
				if err := restoreBackend.Save(context.Background(), snapshot); err != nil {
					t.Fatal(err)
				}
			}
			restored := startAcceptance(t, binary, restoredRoot, restoreBackend, token, true)
			for id, h := range snapshot.HostHistory.Hosts {
				if !acceptancePointsEqual(acceptanceView(h), restored.history(t, id).Points) {
					t.Fatal("restored API changed capped history")
				}
			}
			if !acceptancePointsEqual(expected.Points, restored.history(t, "fixture").Points) {
				t.Fatal("isolated archive restore changed history")
			}
			restored.stop(t, false)
			rollbackRoot := t.TempDir()
			before := unpackAcceptance(t, pre, rollbackRoot, 0)
			var rollbackBackend Backend = NewJSONBackend(filepath.Join(rollbackRoot, "state.json"))
			if mode == "mongo" {
				rollbackBackend = newMongoIntegrationBackend(t)
			}
			if err := rollbackBackend.Save(context.Background(), before); err != nil {
				t.Fatal(err)
			}
			prior := startAcceptance(t, old, rollbackRoot, rollbackBackend, token, false)
			prior.request(t, "GET", "/api/hosts", nil, 200)
			if !bytes.Contains(prior.request(t, "GET", "/api/admin/system-health", nil, 200), []byte("b8bdcbc5bbd72f4799f5ec14d80d8805601cd12c")) {
				t.Fatal("rollback artifact revision mismatch")
			}
			prior.stop(t, false)
			back, err := rollbackBackend.Load(context.Background())
			if err != nil || back.HostHistory != nil || back.Hosts["fixture"] == nil {
				t.Fatal("pre-upgrade rollback failed")
			}
			emitAcceptance(t, "artifact_lifecycle", map[string]any{"backend": mode, "graceful_restart": true, "sigkill_checkpoint": true, "latest_unsaved_survived": recovered.Last == transient.Last, "isolated_archive_restore": true, "pre_upgrade_rollback": true, "online_restore_refused": true, "processes_reaped": true})
		})
	}
}

func acceptancePointsEqual(want, got []historyViewPoint) bool {
	cut := historyCutoff(time.Now().Unix())
	for len(want) > 0 && want[0].T < cut {
		want = want[1:]
	}
	return reflect.DeepEqual(want, got)
}

func acceptanceView(h hostHistory) []historyViewPoint {
	points := []historyViewPoint{}
	for _, p := range h.Points {
		scale := float64(historyScale)
		denom := scale * float64(p.N)
		points = append(points, historyViewPoint{p.T, p.N, float64(p.CPU) / denom, float64(p.CPUMax) / scale, float64(p.Mem) / denom, float64(p.MemMax) / scale, float64(p.Storage) / denom, float64(p.StorageMax) / scale, p.VMs})
	}
	return points
}
