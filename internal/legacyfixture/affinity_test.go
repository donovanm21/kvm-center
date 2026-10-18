package legacyfixture

import (
	"bytes"
	"kvm-center/internal/offlinestaging"
	"os"
	"path/filepath"
	"testing"
)

func TestAffinityConversionPreservesOriginals(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	source := filepath.Join(root, "source")
	if e = os.Mkdir(source, 0700); e != nil {
		t.Fatal(e)
	}
	f := GenerateAffinity()
	if e = Write(source, f); e != nil {
		t.Fatal(e)
	}
	stage := filepath.Join(root, "stage")
	if _, e = offlinestaging.Convert(source, stage); e != nil {
		t.Fatal(e)
	}
	state, e := offlinestaging.ExportLegacyAffinityState(stage)
	if e != nil {
		t.Fatal(e)
	}
	if len(state.Originals) != 2 || state.Reservation != nil {
		t.Fatal("unexpected converted state")
	}
	for _, p := range []string{filepath.Join(source, "affinity_rules.bson"), filepath.Join(stage, "original-affinity_rules.bson")} {
		raw, e := os.ReadFile(p)
		if e != nil || !bytes.Equal(raw, f.Documents["affinity_rules"]) {
			t.Fatal("original changed", e)
		}
	}
}
