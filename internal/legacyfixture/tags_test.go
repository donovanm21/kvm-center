package legacyfixture

import (
	"bytes"
	"go.mongodb.org/mongo-driver/bson"
	"kvm-center/internal/offlinestaging"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLegacyTagConversion(t *testing.T) {
	fixture := GenerateTags()
	root := tagTempDir(t)
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Write(source, fixture); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "stage")
	if _, err := offlinestaging.Convert(source, stage); err != nil {
		t.Fatal(err)
	}
	state, err := offlinestaging.ExportLegacyTagState(stage)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 1 || !reflect.DeepEqual(state.ByVM, map[string][]string{VMID: {"database", "production"}, OrphanTagVMID: {"retained-orphan"}}) {
		t.Fatal("tag semantics changed")
	}
	for _, path := range []string{filepath.Join(source, "vm_tags.bson"), filepath.Join(stage, "original-vm_tags.bson")} {
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(raw, fixture.Documents["vm_tags"]) {
			t.Fatal("original changed", err)
		}
	}
	for _, kind := range []string{"source", "mixed", "unknown", "duplicate", "index"} {
		t.Run(kind, func(t *testing.T) {
			f := GenerateTags()
			switch kind {
			case "source":
				f.Source = DeployedSource
			case "mixed":
				f.Documents["vm_tag_state"] = marshal(bson.D{{Key: "_id", Value: "singleton"}, {Key: "state", Value: bson.D{{Key: "version", Value: int32(1)}}}})
				f.Indexes["vm_tag_state"] = f.Indexes["vm_tags"]
			case "unknown":
				f.Documents["vm_tags"] = marshal(bson.D{{Key: "_id", Value: VMID}, {Key: "tags", Value: bson.A{"a"}}, {Key: "future", Value: true}})
			case "duplicate":
				f.Documents["vm_tags"] = append(f.Documents["vm_tags"], f.Documents["vm_tags"]...)
			case "index":
				f.Indexes["vm_tags"] = append(f.Indexes["vm_tags"], marshal(bson.D{{Key: "v", Value: int32(2)}, {Key: "name", Value: "tags_1"}, {Key: "key", Value: bson.D{{Key: "tags", Value: int32(1)}}}})...)
			}
			dir := tagTempDir(t)
			if err := Write(dir, f); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(tagTempDir(t), "stage")
			_, err := offlinestaging.Convert(dir, stage)
			if err != nil {
				if kind == "duplicate" || kind == "index" {
					return
				}
				t.Fatal(err)
			}
			if _, err := offlinestaging.ExportLegacyTagState(stage); err == nil {
				t.Fatal("unsafe export admitted")
			}
		})
	}
}

func tagTempDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
