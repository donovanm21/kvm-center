package legacyfixture

import (
	"bytes"
	"encoding/json"
	"kvm-center/internal/offlinestaging"
	"os"
	"path/filepath"
	"testing"
)

func TestSupportedCompositionAccounting(t *testing.T) {
	a, e := CompositionAccounting()
	if e != nil {
		t.Fatal(e)
	}
	got, e := json.MarshalIndent(a, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	want, e := os.ReadFile("../../docs/supported-composition-v1-accounting.json")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(append(got, '\n'), want) {
		t.Fatal("composition field/index contract changed; version and review accounting explicitly")
	}
	if len(a) != 16 {
		t.Fatal("composition collection count")
	}
}
func TestSupportedCompositionPreservation(t *testing.T) {
	for name, b := range CompositionParts() {
		t.Run(name, func(t *testing.T) {
			root, e := filepath.EvalSymlinks(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			src := filepath.Join(root, "source")
			if e = os.Mkdir(src, 0700); e != nil {
				t.Fatal(e)
			}
			if e = Write(src, b); e != nil {
				t.Fatal(e)
			}
			stage := filepath.Join(root, "stage")
			m, e := offlinestaging.Convert(src, stage)
			if e != nil {
				t.Fatal(e)
			}
			if m.Activation {
				t.Fatal("activation")
			}
			if _, e = offlinestaging.Verify(stage); e != nil {
				t.Fatal(e)
			}
			for col, old := range b.Documents {
				for _, p := range []string{filepath.Join(src, col+".bson"), filepath.Join(stage, "original-"+col+".bson")} {
					raw, e := os.ReadFile(p)
					if e != nil || !bytes.Equal(raw, old) {
						t.Fatalf("component changed %s %v", col, e)
					}
				}
			}
		})
	}
}

func TestSupportedCompositionUnknownVersionRefused(t *testing.T) {
	b := GenerateCompositionEntities()
	b.Schema = "synthetic-supported-composition-v2"
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	src := filepath.Join(root, "source")
	if e = os.Mkdir(src, 0700); e != nil {
		t.Fatal(e)
	}
	if e = Write(src, b); e != nil {
		t.Fatal(e)
	}
	if _, e = offlinestaging.Convert(src, filepath.Join(root, "stage")); e == nil {
		t.Fatal("unknown composition version accepted")
	}
}
