package legacyfixture

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/models"
	"kvm-center/internal/offlinestaging"
)

func TestPinnedLegacyPreservation(t *testing.T) {
	b := Generate()
	second := Generate()
	for name, raw := range b.Documents {
		if !bytes.Equal(raw, second.Documents[name]) {
			t.Fatalf("nondeterministic fixture: %s", name)
		}
	}
	if len(b.Documents) != 30 {
		t.Fatalf("collections=%d", len(b.Documents))
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "source")
	if err := os.Mkdir(src, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Write(src, b); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "stage")
	m, err := offlinestaging.Convert(src, stage)
	if err != nil {
		t.Fatal(err)
	}
	if m.Activation || m.Status != "VERIFIED_OFFLINE" {
		t.Fatal("unexpected activation")
	}
	for name, original := range b.Documents {
		for _, path := range []string{filepath.Join(src, name+".bson"), filepath.Join(stage, "original-"+name+".bson"), filepath.Join(stage, "normalized-"+name+".bson")} {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("exact canonical fixture changed: %s %v", name, err)
			}
		}
	}
	history, err := offlinestaging.History(stage)
	if err != nil || len(history) != 1 || !bytes.Equal(history[0].Raw, b.Documents["perf_history"]) {
		t.Fatalf("history changed: %v", err)
	}
	// Restore the raw originals to a new offline source, not an application backup.
	restore := filepath.Join(root, "restored")
	if err := os.Mkdir(restore, 0700); err != nil {
		t.Fatal(err)
	}
	for name := range b.Checksums {
		archiveName := "original-" + name
		if strings.HasSuffix(name, ".indexes.bson") {
			archiveName = "indexes-" + strings.TrimSuffix(name, ".indexes.bson") + ".bson"
		}
		raw, err := os.ReadFile(filepath.Join(stage, archiveName))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(restore, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(stage, "source.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(restore, "snapshot.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for name, want := range b.Checksums {
		got, err := os.ReadFile(filepath.Join(restore, name))
		if err != nil || hash(got) != want {
			t.Fatalf("restore changed %s", name)
		}
	}
	again, err := offlinestaging.Convert(restore, filepath.Join(root, "restaged"))
	if err != nil || again.SourceSHA256 != m.SourceSHA256 {
		t.Fatalf("restored source differs: %v", err)
	}
	var vm VM
	var command CommandLogEntry
	var migration MigrationRecord
	for name, target := range map[string]any{"vms": &vm, "command_log": &command, "migrations": &migration} {
		if err := bson.Unmarshal(b.Documents[name], target); err != nil {
			t.Fatal(err)
		}
	}
	if command.VMName != vm.Name || migration.VMName != vm.Name || command.VMUUID != vm.UUID || migration.RequestID != command.RequestID || command.LastProgress == nil || command.LastProgress.RequestID != command.RequestID {
		t.Fatal("broken command/VM references")
	}
	wantExpiry := time.Unix(migration.CompletedAt, 0).Add(30 * 24 * time.Hour).UnixMilli()
	if bson.Raw(b.Documents["migrations"]).Lookup("expires_at").DateTime() != wantExpiry {
		t.Fatal("migration expiry changed units")
	}
	// Explicitly prove why successful typed decoding is NOT compatibility.
	for name, target := range map[string]any{"hosts": &models.Host{}, "vms": &models.VM{}} {
		if err := bson.Unmarshal(b.Documents[name], target); err != nil {
			t.Fatal(err)
		}
		encoded, err := bson.Marshal(target)
		if err != nil {
			t.Fatal(err)
		}
		knownLegacy := map[string]string{"hosts": "agent_tls_port", "vms": "haspassthrough"}[name]
		if bson.Raw(b.Documents[name]).Lookup(knownLegacy).Type == 0 || bson.Raw(encoded).Lookup(knownLegacy).Type != 0 {
			t.Fatal("known legacy gap accounting changed")
		}
		if bson.Raw(encoded).Lookup("unsupported_extra").Type != 0 {
			t.Fatal("update expected unsupported accounting")
		}
	}
	evidence, _ := json.Marshal(map[string]any{"schema": Schema, "source": Source, "collections": len(b.Documents), "original_checksums": b.Checksums, "offline_preserved": true, "history_raw_unchanged": true, "raw_restore_reconversion": true, "active_compatible": false, "refused_semantics": []string{"unknown fields/collection", "alarms/affinity", "extended host/VM/guest fields", "legacy tiered history and tags", "expiry and executable jobs"}})
	t.Log("LEGACY_FIXTURE_EVIDENCE " + string(evidence))
}
