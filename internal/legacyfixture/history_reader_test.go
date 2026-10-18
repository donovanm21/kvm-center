package legacyfixture

import (
	"bytes"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/offlinestaging"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHistoryReaderConversionAndRecovery(t *testing.T) {
	b := GenerateHistoryReader()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Write(source, b); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "stage")
	if _, err := offlinestaging.Convert(source, stage); err != nil {
		t.Fatal(err)
	}
	if _, err := offlinestaging.Verify(stage); err != nil {
		t.Fatal(err)
	}
	got, err := offlinestaging.LegacyHistory(stage)
	if err != nil || len(got) != 1 || len(got[0].Tiers) != 3 || len(got[0].Tiers[0].Points) != 2 {
		t.Fatal("read", err)
	}
	if got[0].Tiers[0].Points[0].N != 2 || got[0].Tiers[0].Points[0].VMs != 3 || got[0].Tiers[0].Points[0].CPUAvg != 20 || got[0].Tiers[1].Points[0].N != 3 || got[0].Tiers[1].Points[0].CPUAvg != 30 {
		t.Fatal("sample meanings")
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
	if _, err := offlinestaging.Convert(restored, recovered); err != nil {
		t.Fatal(err)
	}
	back, err := offlinestaging.LegacyHistory(recovered)
	if err != nil || !reflect.DeepEqual(got, back) {
		t.Fatal("restore", err)
	}
	for _, dir := range []string{source, restored} {
		raw, err := os.ReadFile(filepath.Join(dir, "perf_history.bson"))
		if err != nil || !bytes.Equal(raw, b.Documents["perf_history"]) {
			t.Fatal("original changed")
		}
	}
	for _, kind := range []string{"source", "mixed", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			b := GenerateHistoryReader()
			switch kind {
			case "source":
				b.Source = "unverified-source"
			case "mixed":
				b.Documents["host_history_state"], _ = bson.Marshal(bson.D{{Key: "_id", Value: "singleton"}})
				b.Indexes["host_history_state"] = b.Indexes["perf_history"]
			case "unknown":
				b = GenerateDeployed()
			}
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := Write(dir, b); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "refusal-"+kind)
			if _, err := offlinestaging.Convert(dir, out); err != nil {
				t.Fatal(err)
			}
			if result, err := offlinestaging.LegacyHistory(out); err == nil || result != nil {
				t.Fatal("unsupported interpretation accepted")
			}
			if result, err := offlinestaging.History(out); err != nil || len(result) == 0 {
				t.Fatal("raw reader lost", err)
			}
		})
	}
	// Source manifests are declarations, never executable provenance evidence.
	raw, _ := json.Marshal(got)
	if bytes.Contains(raw, []byte(`"active_compatible":true`)) {
		t.Fatal("activation claim")
	}
	t.Logf("HISTORY_READER_FIXTURE %s", b.Checksums["perf_history.bson"])
}
