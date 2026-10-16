package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kvm-center/internal/models"
)

type bootFixtureBackend struct {
	ready            bool
	data             *persistData
	loadErr, saveErr error
	loads, closes    int
	failLoadAt       int
}

func (b *bootFixtureBackend) Name() string { return "mongo" }
func (b *bootFixtureBackend) Info(context.Context) BackendInfo {
	return BackendInfo{Healthy: b.ready, Error: "mongodb://synthetic:fixture-secret@invalid"}
}
func (b *bootFixtureBackend) Load(context.Context) (*persistData, error) {
	b.loads++
	if b.failLoadAt == 0 || b.loads == b.failLoadAt {
		if b.loadErr != nil {
			return nil, b.loadErr
		}
	}
	return b.data, nil
}
func (b *bootFixtureBackend) Save(_ context.Context, d *persistData) error {
	if b.saveErr != nil {
		return b.saveErr
	}
	b.data = d
	return nil
}
func (b *bootFixtureBackend) Close(context.Context) error { b.closes++; return nil }
func bootNoDelay(context.Context) error                   { return nil }
func bootData() *persistData {
	return &persistData{Hosts: map[string]*models.Host{"synthetic-host": {ID: "synthetic-host"}}}
}

func TestMongoBootstrapNeverFallsBack(t *testing.T) {
	for _, populated := range []bool{false, true} {
		for _, marker := range []bool{false, true} {
			t.Run(fmt.Sprintf("json=%t/marker=%t", populated, marker), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "data.json")
				original := []byte(`{"hosts":{"synthetic-host":{"id":"synthetic-host"}}}`)
				if populated {
					if err := os.WriteFile(path, original, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if marker {
					if err := os.WriteFile(path+".backend", []byte("mongo\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				var attempts []*bootFixtureBackend
				var logs bytes.Buffer
				prior := log.Writer()
				log.SetOutput(&logs)
				defer log.SetOutput(prior)
				b, err := bootstrapBackendWith(context.Background(), "mongodb://synthetic:fixture-secret@invalid", path, func() Backend { f := &bootFixtureBackend{}; attempts = append(attempts, f); return f }, bootNoDelay)
				if b != nil || err == nil || len(attempts) != 5 {
					t.Fatalf("backend=%v err=%v attempts=%d", b, err, len(attempts))
				}
				for _, f := range attempts {
					if f.closes != 1 {
						t.Fatal("failed client not closed")
					}
				}
				if strings.Contains(err.Error()+logs.String(), "fixture-secret") {
					t.Fatal("connection details leaked")
				}
				got, readErr := os.ReadFile(path)
				if populated && (readErr != nil || !bytes.Equal(got, original)) {
					t.Fatal("JSON source changed")
				}
				if !populated && !os.IsNotExist(readErr) {
					t.Fatal("JSON source created")
				}
			})
		}
	}
}

func TestMongoBootstrapLoadMigrationAndHydrationFailures(t *testing.T) {
	for _, stage := range []string{"load", "migration", "hydration"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.json")
			original := []byte(`{"hosts":{"synthetic-host":{"id":"synthetic-host"}}}`)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("mongodb://synthetic:fixture-secret@invalid")
			f := &bootFixtureBackend{ready: true, data: bootData()}
			switch stage {
			case "load":
				f.loadErr = failure
			case "migration":
				f.data = &persistData{}
				f.saveErr = failure
			case "hydration":
				f.failLoadAt = 2
				f.loadErr = failure
			}
			var logs bytes.Buffer
			prior := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(prior)
			b, err := bootstrapBackendWith(context.Background(), "configured", path, func() Backend { return f }, bootNoDelay)
			if stage == "hydration" {
				if err != nil {
					t.Fatal(err)
				}
				_, err = initializeBootStore(b, path)
			}
			if err == nil || f.closes != 1 {
				t.Fatalf("err=%v closes=%d", err, f.closes)
			}
			if strings.Contains(err.Error()+logs.String(), "fixture-secret") {
				t.Fatal("connection details leaked")
			}
			got, _ := os.ReadFile(path)
			if !bytes.Equal(got, original) {
				t.Fatal("JSON source changed")
			}
			if _, err := os.Stat(path + ".backend"); !os.IsNotExist(err) {
				t.Fatal("failed startup recorded success")
			}
		})
	}
}

func TestMongoBootstrapRetryThenSuccessAndSelectionMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	attempts := 0
	f := &bootFixtureBackend{ready: true, data: bootData()}
	b, err := bootstrapBackendWith(context.Background(), "configured", path, func() Backend {
		attempts++
		if attempts < 3 {
			return &bootFixtureBackend{}
		}
		return f
	}, bootNoDelay)
	if err != nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	if _, err := os.Stat(path + ".backend"); !os.IsNotExist(err) {
		t.Fatal("marker written before hydration")
	}
	s, err := initializeBootStore(b, path)
	if err != nil || len(s.hosts) != 1 {
		t.Fatalf("hydrate: %v", err)
	}
	closeBootBackend(b)
	marker, _ := os.ReadFile(path + ".backend")
	if string(marker) != "mongo\n" {
		t.Fatal("missing selection marker")
	}
	b, err = bootstrapBackendWith(context.Background(), "", path, nil, nil)
	if err == nil || b != nil {
		t.Fatal("missing Mongo configuration selected JSON")
	}
}

func TestBootstrapIntentionalJSONAndInvalidMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	b, err := bootstrapBackendWith(context.Background(), "", path, nil, nil)
	if err != nil || b.Name() != "json" {
		t.Fatalf("JSON selection: %v", err)
	}
	if _, err := initializeBootStore(b, path); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"", "unknown", "mongo"} {
		if err := os.WriteFile(path+".backend", []byte(marker), 0600); err != nil {
			t.Fatal(err)
		}
		if b, err := bootstrapBackendWith(context.Background(), "", path, nil, nil); b != nil || err == nil {
			t.Fatalf("accepted marker %q", marker)
		}
	}
	if err := os.WriteFile(path+".backend", []byte("json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if b, err := bootstrapBackendWith(context.Background(), "", path, nil, nil); err != nil || b.Name() != "json" {
		t.Fatal("explicit JSON marker rejected")
	}
}

func TestMongoBootstrapCancellationAndMarkerFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &bootFixtureBackend{}
	if b, err := bootstrapBackendWith(ctx, "configured", path, func() Backend { return f }, bootNoDelay); b != nil || err == nil || f.closes != 1 {
		t.Fatal("cancellation did not close failed client")
	}
	if err := os.Mkdir(path+".backend", 0700); err != nil {
		t.Fatal(err)
	}
	f = &bootFixtureBackend{ready: true, data: bootData()}
	if s, err := initializeBootStore(f, path); s != nil || err == nil || f.closes != 1 {
		t.Fatal("marker failure allowed startup")
	}
}

func TestMongoBootstrapChildMain(t *testing.T) {
	root := os.Getenv("KVMC_BOOT_CHILD_ROOT")
	if root == "" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("boot-fixture", flag.ExitOnError)
	os.Args = []string{"boot-fixture", "-no-tls", "-addr", "127.0.0.1:0", "-grpc-addr", "", "-data", filepath.Join(root, "data.json"), "-ca-dir", filepath.Join(root, "ca"), "-tls-dir", filepath.Join(root, "tls"), "-mongo-url", os.Getenv("KVMC_BOOT_CHILD_URI")}
	main()
}

// Execute actual main: startup must exit before listener/CA/background startup,
// leaving a populated legacy file untouched even when the URI is unusable.
func TestMongoBootstrapProcessStopsBeforeServing(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprintf("configured=%t", configured), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "data.json")
			original := []byte(`{"hosts":{"synthetic-host":{"id":"synthetic-host"}}}`)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".backend", []byte("mongo\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMongoBootstrapChildMain$")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "KVM_CENTER_") && !strings.HasPrefix(entry, "KVMC_BOOT_CHILD_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "KVMC_BOOT_CHILD_ROOT="+root)
			if configured {
				cmd.Env = append(cmd.Env, "KVMC_BOOT_CHILD_URI=invalid://synthetic:fixture-secret@invalid")
			}
			out, err := cmd.CombinedOutput()
			if err == nil || ctx.Err() != nil {
				t.Fatalf("expected bounded nonzero exit, err=%v context=%v", err, ctx.Err())
			}
			if !bytes.Contains(out, []byte("backend startup:")) || bytes.Contains(out, []byte("backend: using")) || bytes.Contains(out, []byte("fixture-secret")) {
				t.Fatalf("unexpected startup output: %s", out)
			}
			got, _ := os.ReadFile(path)
			if !bytes.Equal(got, original) {
				t.Fatal("legacy JSON changed")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 2 {
				t.Fatal("startup created runtime files")
			}
		})
	}
}
