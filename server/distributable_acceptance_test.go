//go:build integration && historyacceptance && packagingacceptance && linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
)

func packageDocker(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("owned Docker operation %s failed: %v", args[0], err)
	}
	return out
}

func startPackage(t *testing.T, image, name, volume string, b *MongoBackend, token string) *acceptanceProcess {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	args := []string{"run", "--rm", "--name", name, "--network", "host", "--mount", "type=volume,src=" + volume + ",dst=/var/lib/kvm-center",
		"-e", "KVM_CENTER_MONGO_URL=" + b.uri, "-e", "KVM_CENTER_MONGO_DB=" + b.dbName,
		image, "/app/kvm-center-server", "-no-tls", "-addr", addr, "-tls-addr", "127.0.0.1:0", "-grpc-addr", "",
		"-ca-dir", "/var/lib/kvm-center/ca", "-tls-dir", "/var/lib/kvm-center/tls", "-graceful-shutdown"}
	p := &acceptanceProcess{cmd: exec.Command("docker", args...), done: make(chan error, 1), client: &http.Client{Timeout: 10 * time.Second}, url: "http://" + addr, token: token}
	log, err := os.Create(filepath.Join(t.TempDir(), "private-process.log"))
	if err != nil {
		t.Fatal(err)
	}
	p.cmd.Stdout = log
	p.cmd.Stderr = log
	if err = p.cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	go func() { p.done <- p.cmd.Wait(); log.Close() }()
	t.Cleanup(func() { p.stop(t, true) })
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", p.url+"/api/admin/system-health", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := p.client.Do(req)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				return p
			}
		}
		select {
		case <-p.done:
			p.stopped = true
			t.Fatal("image exited before authenticated readiness")
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("image readiness timeout")
	return nil
}

func TestDistributableRuntime(t *testing.T) {
	image := os.Getenv("KVMC_PACKAGE_IMAGE")
	sha := os.Getenv("KVMC_PACKAGE_SHA")
	version := os.Getenv("KVMC_PACKAGE_VERSION")
	if !strings.HasPrefix(image, "kvmc-validation:") || len(sha) != 40 || version == "" {
		t.Fatal("explicit owned image and source identity required")
	}
	b := newMongoIntegrationBackend(t)
	root := t.TempDir()
	s, token := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	s.mu.Lock()
	s.hosts["fixture-host"] = &models.Host{ID: "fixture-host", IP: "192.0.2.10", Status: models.HostOffline}
	s.vms["fixture-host"] = []*models.VM{{UUID: "00000000-0000-4000-8000-000000000001", Name: "fixture-vm", HostID: "fixture-host", State: models.VMStopped, MemTotalMB: 256}}
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	name := os.Getenv("KVMC_PACKAGE_CONTAINER")
	volume := os.Getenv("KVMC_PACKAGE_VOLUME")
	if !strings.HasPrefix(name, "kvmc-validation-runtime-") || volume != name+"-data" {
		t.Fatal("explicit owned resource names required")
	}
	packageDocker(t, "volume", "create", volume)
	// Remove only these exact test-owned resources, including when the CLI was killed.
	t.Cleanup(func() { packageDocker(t, "volume", "rm", volume) })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	})
	p := startPackage(t, image, name, volume, b, token)
	check := func(p *acceptanceProcess) {
		health := p.request(t, "GET", "/api/admin/system-health", nil, 200)
		if !bytes.Contains(health, []byte(sha)) || !bytes.Contains(health, []byte(version)) {
			t.Fatal("runtime version/SHA mismatch")
		}
		if !bytes.Contains(p.request(t, "GET", "/api/hosts", nil, 200), []byte("fixture-host")) {
			t.Fatal("Mongo host missing")
		}
		if !bytes.Contains(p.request(t, "GET", "/api/vms", nil, 200), []byte("fixture-vm")) {
			t.Fatal("Mongo VM missing")
		}
	}
	check(p)
	status := string(packageDocker(t, "exec", name, "cat", "/proc/1/status"))
	if !regexp.MustCompile(`(?m)^Uid:\s+[1-9][0-9]*\s+`).MatchString(status) {
		t.Fatal("runtime process did not drop root")
	}
	marker := strings.TrimSpace(string(packageDocker(t, "exec", name, "cat", "/var/lib/kvm-center/data.json.backend")))
	if marker != "mongo" {
		t.Fatal("intended Mongo backend marker absent")
	}
	html := p.request(t, "GET", "/", nil, 200)
	asset := regexp.MustCompile(`(?:src|href)="(/assets/[^" ]+\.js)"`).FindSubmatch(html)
	if len(asset) != 2 {
		t.Fatal("fresh embedded SPA JavaScript reference missing")
	}
	js := p.request(t, "GET", string(asset[1]), nil, 200)
	if len(js) < 1000 || bytes.HasPrefix(bytes.TrimSpace(js), []byte("<!doctype")) {
		t.Fatal("SPA asset missing or HTML fallback")
	}
	download := p.request(t, "GET", "/downloads/kvmctl-linux-amd64", nil, 200)
	if !bytes.HasPrefix(download, []byte{0x7f, 'E', 'L', 'F'}) {
		t.Fatal("actual download binary missing")
	}
	packageDocker(t, "exec", name, "/app/kvm-healthcheck", "-h")
	p.stop(t, false)
	p = startPackage(t, image, name, volume, b, token)
	check(p)
	p.stop(t, false)
	raw, _ := json.Marshal(map[string]any{"mongo_startup": true, "authenticated_restart": true, "nonroot": true, "spa_asset": string(asset[1]), "download_elf": true, "healthcheck_help_executed": true, "compatibility_scope": "current public shape only; separate v5.61.58 refusal below"})
	t.Log("PACKAGE_EVIDENCE " + string(raw))
}

// Hand-written BSON names from the v5.61.58 persistence contract, never a
// production export or a fixture serialized through today's models.
func TestDistributableLegacyRefusal(t *testing.T) {
	b := newMongoIntegrationBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := b.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]bson.M{
		"hosts":          {"id": "legacy-fixture", "ip": "192.0.2.20", "status": "offline", "agent_tls_port": 9443},
		"vms":            {"uuid": "00000000-0000-4000-8000-000000000002", "host_id": "legacy-fixture", "name": "legacy-fixture-vm", "state": "stopped", "haspassthrough": true},
		"sessions":       {"token": "synthetic-unused-session", "userid": "synthetic-unused-user", "expires_at": time.Now().Add(time.Hour)},
		"migrations":     {"id": "synthetic-migration", "started_at": int64(1700000000)},
		"audit_events":   {"id": "synthetic-audit", "actor_id": "synthetic-unused-user"},
		"alarms":         {"_id": "singleton", "rules": bson.A{}},
		"affinity_rules": {"_id": "singleton", "rules": bson.A{}},
		"perf_history":   {"host": "legacy-fixture", "points": bson.A{}},
	}
	before := map[string]bson.Raw{}
	for collection, doc := range fixtures {
		if _, err := db.Collection(collection).InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	for collection := range fixtures {
		var raw bson.Raw
		if err := db.Collection(collection).FindOne(ctx, bson.D{}).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		before[collection] = append(bson.Raw(nil), raw...)
	}
	if _, err := b.Load(ctx); err == nil || !strings.Contains(err.Error(), "explicit offline conversion") {
		t.Fatal("legacy history must explicitly refuse loading")
	}
	// Independently expose shared-field decode incompatibility without writing
	// the decoded state back. These findings block release, not this packaging gate.
	fields := []struct {
		name     string
		doc      bson.M
		target   any
		retained func() bool
	}{}
	var vm models.VM
	var session models.Session
	var migration models.MigrationRecord
	var audit models.AuditEvent
	fields = append(fields, struct {
		name     string
		doc      bson.M
		target   any
		retained func() bool
	}{"VM.host_id", fixtures["vms"], &vm, func() bool { return vm.HostID == "legacy-fixture" }})
	fields = append(fields, struct {
		name     string
		doc      bson.M
		target   any
		retained func() bool
	}{"Session.expires_at", fixtures["sessions"], &session, func() bool { return !session.ExpiresAt.IsZero() }})
	fields = append(fields, struct {
		name     string
		doc      bson.M
		target   any
		retained func() bool
	}{"Migration.started_at", fixtures["migrations"], &migration, func() bool { return migration.StartedAt != 0 }})
	fields = append(fields, struct {
		name     string
		doc      bson.M
		target   any
		retained func() bool
	}{"Audit.actor_id", fixtures["audit_events"], &audit, func() bool { return audit.ActorID != "" }})
	findings := map[string]bool{}
	for _, f := range fields {
		raw, err := bson.Marshal(f.doc)
		if err != nil {
			t.Fatal(err)
		}
		if err = bson.Unmarshal(raw, f.target); err != nil {
			t.Fatal(err)
		}
		findings[f.name] = f.retained()
	}
	for collection := range fixtures {
		n, err := db.Collection(collection).CountDocuments(ctx, bson.D{})
		if err != nil || n != 1 {
			t.Fatal("read-only compatibility assessment changed fixture counts")
		}
		var raw bson.Raw
		if err := db.Collection(collection).FindOne(ctx, bson.D{}).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before[collection], raw) {
			t.Fatal("read-only compatibility assessment changed fixture document")
		}
	}
	raw, _ := json.Marshal(map[string]any{"legacy_history_refused": true, "v5_61_58_compatible": false, "shared_fields_retained": findings, "fixture_source": "synthetic BSON contract; no deployed data", "documents_unchanged": true})
	t.Log("PACKAGE_EVIDENCE " + string(raw))
}
