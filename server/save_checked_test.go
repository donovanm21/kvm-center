package main

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"

	"kvm-center/internal/models"
)

func TestSaveChecked_ReportsBackendErrorAndRetainsDirtyState(t *testing.T) {
	s, b := persistenceTestStore(t)
	want := errors.New("storage unavailable")
	b.saveErr = want
	s.mu.Lock()
	s.roles["durable-role"] = &models.Role{ID: "durable-role"}
	s.mu.Unlock()
	if err := s.SaveChecked(); !errors.Is(err, want) {
		t.Fatalf("SaveChecked error=%v, want %v", err, want)
	}
	b.saveErr = nil
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	if len(b.attempts) != 2 || !isDirty(b.attempts[1], "roles") {
		t.Fatal("failed durable save was not retried")
	}
}

func TestSaveChecked_RejectsUnavailablePersistence(t *testing.T) {
	for _, s := range []*Store{{}, {backend: &stubBackend{name: "unloaded"}}} {
		if err := s.SaveChecked(); err == nil {
			t.Fatal("SaveChecked claimed success without a loaded backend")
		}
	}
}

func TestSaveChecked_ReportsSnapshotFailure(t *testing.T) {
	s, b := persistenceTestStore(t)
	s.mu.Lock()
	s.hosts["invalid"] = &models.Host{ID: "invalid", CPUUsage: math.NaN()}
	s.mu.Unlock()
	if err := s.SaveChecked(); err == nil {
		t.Fatal("unserializable snapshot reported success")
	}
	if len(b.attempts) != 0 {
		t.Fatal("invalid snapshot reached backend")
	}
}

func TestJSONBackend_ReportsDirectorySyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	b := NewJSONBackend(path)
	want := errors.New("directory sync failed")
	called := false
	err := b.saveWithDirSync(&persistData{ConsoleSecret: "persisted"}, func(dir string) error {
		called = true
		if dir != filepath.Dir(path) {
			t.Errorf("sync target=%q", dir)
		}
		// Directory sync must follow rename; the new file already exists.
		got, readErr := b.Load(context.Background())
		if readErr != nil || got.ConsoleSecret != "persisted" {
			t.Errorf("sync ran before rename: %v", readErr)
		}
		return want
	})
	if !called || !errors.Is(err, want) {
		t.Fatalf("sync failure was not propagated: called=%v err=%v", called, err)
	}
}
