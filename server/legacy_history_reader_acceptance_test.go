//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/offlinestaging"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestHistoryAcceptanceLegacyHistoryReader(t *testing.T) {
	binary := os.Getenv("KVMC_OFFLINE_BINARY")
	if binary == "" {
		t.Fatal("offline artifact required")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	fixture := legacyfixture.GenerateHistoryReader()
	if err := legacyfixture.Write(source, fixture); err != nil {
		t.Fatal(err)
	}
	original := fixture.Documents["perf_history"]
	for _, name := range []string{"perf_history.bson", "perf_history.indexes.bson", "snapshot.json"} {
		if err := os.Chmod(filepath.Join(source, name), 0400); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, binary, args...).Output()
		if err != nil {
			t.Fatal("offline artifact operation failed", err)
		}
		return out
	}
	stage := filepath.Join(root, "stage")
	run("-mode", "convert", "-source", source, "-bundle", stage)
	view1 := run("-mode", "legacy-history", "-bundle", stage)
	view2 := run("-mode", "legacy-history", "-bundle", stage)
	if !bytes.Equal(view1, view2) {
		t.Fatal("fresh process changed interpretation")
	}
	var views []offlinestaging.LegacyHistoryView
	if json.Unmarshal(view1, &views) != nil || len(views) != 1 {
		t.Fatal("reader result")
	}
	v := views[0]
	if v.Schema != offlinestaging.LegacyHistorySchema || v.OriginalSHA256 != fixture.Checksums["perf_history.bson"] || v.ActiveCompatible || len(v.Tiers) != 3 {
		t.Fatal("reader contract")
	}
	want := []offlinestaging.LegacyHistoryPoint{{T: 1704067200, CPUAvg: 20, CPUMax: 30, MemAvg: 30, MemMax: 40, StorageAvg: 40, StorageMax: 50, VMs: 3, N: 2}, {T: 1704067500, CPUAvg: 50, CPUMax: 50, MemAvg: 60, MemMax: 60, StorageAvg: 70, StorageMax: 70, VMs: 4, N: 1}}
	coarse := offlinestaging.LegacyHistoryPoint{T: 1704067200, CPUAvg: 30, CPUMax: 50, MemAvg: 40, MemMax: 60, StorageAvg: 50, StorageMax: 70, VMs: 4, N: 3}
	if !reflect.DeepEqual(v.Tiers[0].Points, want) || !reflect.DeepEqual(v.Tiers[1].Points, []offlinestaging.LegacyHistoryPoint{coarse}) || !reflect.DeepEqual(v.Tiers[2].Points, []offlinestaging.LegacyHistoryPoint{coarse}) {
		t.Fatal("stored aggregates were changed")
	}
	if !bytes.Equal(run("-mode", "history", "-raw-history", "-bundle", stage), original) {
		t.Fatal("raw reader changed history")
	}
	restored := filepath.Join(root, "restored")
	if err := os.Mkdir(restored, 0700); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"source.json", "snapshot.json"}, {"original-perf_history.bson", "perf_history.bson"}, {"indexes-perf_history.bson", "perf_history.indexes.bson"}} {
		raw, err := os.ReadFile(filepath.Join(stage, pair[0]))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(restored, pair[1]), raw, 0400); err != nil {
			t.Fatal(err)
		}
	}
	recovered := filepath.Join(root, "recovered")
	run("-mode", "convert", "-source", restored, "-bundle", recovered)
	if !bytes.Equal(view1, run("-mode", "legacy-history", "-bundle", recovered)) {
		t.Fatal("restored reader changed")
	}
	for _, path := range []string{filepath.Join(source, "perf_history.bson"), filepath.Join(stage, "original-perf_history.bson"), filepath.Join(recovered, "original-perf_history.bson")} {
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(raw, original) {
			t.Fatal("original changed")
		}
	}
	// Full fixture stays intact and continues to refuse unknown history semantics.
	full := filepath.Join(root, "full")
	if err := os.Mkdir(full, 0700); err != nil {
		t.Fatal(err)
	}
	if err := legacyfixture.Write(full, legacyfixture.GenerateDeployed()); err != nil {
		t.Fatal(err)
	}
	fullStage := filepath.Join(root, "full-stage")
	run("-mode", "convert", "-source", full, "-bundle", fullStage)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "-mode", "legacy-history", "-bundle", fullStage).Output()
	if err == nil || len(out) != 0 {
		t.Fatal("full fixture unsupported semantics accepted or partial output emitted")
	}
	if len(run("-mode", "history", "-raw-history", "-bundle", fullStage)) == 0 {
		t.Fatal("refusal lost raw reader")
	}
	emitAcceptance(t, "legacy_history_reader", map[string]any{"backend": "offline", "fixture_schema": fixture.Schema, "source_contract": fixture.Source, "original_sha256": fixture.Checksums["perf_history.bson"], "fresh_process_reopen": true, "fresh_restore_reconversion": true, "stored_aggregates_exact": true, "all_three_tiers": true, "old_points_not_expired": true, "originals_unchanged": true, "full_fixture_unknown_refused": true, "raw_reader_retained": true, "active_compatible": false, "application_history_conversion": false, "old_writer_rollback": false})
}
