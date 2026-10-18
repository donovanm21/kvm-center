package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"kvm-center/internal/legacyfixture"
	"kvm-center/internal/offlinestaging"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Independent refusal fixtures, never full-schema fixtures stripped to boot.
func unsupportedNamespaceFixture(t *testing.T, name string) legacyfixture.Bundle {
	t.Helper()
	docs := map[string]bson.D{
		name:       {{Key: "_id", Value: "synthetic-unsupported"}, {Key: "id", Value: "synthetic-unsupported"}, {Key: "enabled", Value: true}, {Key: "opaque", Value: primitive.NewDecimal128(73, 79)}},
		"sessions": {{Key: "_id", Value: "synthetic-expired"}, {Key: "expires_at", Value: time.Unix(1, 0)}},
	}
	b := legacyfixture.Bundle{Schema: "synthetic-unsupported-namespace-bson-v1", Source: legacyfixture.DeployedSource, Documents: map[string][]byte{}, Indexes: map[string][]byte{}, Checksums: map[string]string{}}
	for col, doc := range docs {
		raw, err := bson.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		b.Documents[col] = raw
		index, err := bson.Marshal(bson.D{{Key: "v", Value: int32(2)}, {Key: "name", Value: "_id_"}, {Key: "key", Value: bson.D{{Key: "_id", Value: int32(1)}}}})
		if err != nil {
			t.Fatal(err)
		}
		b.Indexes[col] = index
		for file, data := range map[string][]byte{col + ".bson": raw, col + ".indexes.bson": index} {
			sum := sha256.Sum256(data)
			b.Checksums[file] = hex.EncodeToString(sum[:])
		}
	}
	return b
}

func TestUnsupportedNamespaceOfflinePreservation(t *testing.T) {
	for _, name := range []string{"alarms", "affinity_rules", "future_feature"} {
		t.Run(name, func(t *testing.T) {
			fixture := unsupportedNamespaceFixture(t, name)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			src, stage := filepath.Join(root, "originals"), filepath.Join(root, "stage")
			if err := os.Mkdir(src, 0700); err != nil {
				t.Fatal(err)
			}
			if err := legacyfixture.Write(src, fixture); err != nil {
				t.Fatal(err)
			}
			if _, err := offlinestaging.Convert(src, stage); err != nil {
				t.Fatal(err)
			}
			if _, err := offlinestaging.Verify(stage); err != nil {
				t.Fatal(err)
			}
			for col, original := range fixture.Documents {
				for _, path := range []string{filepath.Join(src, col+".bson"), filepath.Join(stage, "normalized-"+col+".bson")} {
					got, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(got, original) {
						t.Fatal("offline preservation changed BSON", err)
					}
				}
			}
		})
	}
}
