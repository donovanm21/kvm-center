package main

import (
	"testing"
	"time"

	"kvm-center/internal/models"
)

// TestPrunePhantomHosts_DropsOldOfflineHost: a host last seen >
// phantomHostPruneAfter ago is removed along with its VMs/storage.
func TestPrunePhantomHosts_DropsOldOfflineHost(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	// Drain fire-and-forget AsyncSave goroutines before t.TempDir
	// cleanup — otherwise the rename into a removed dir trips
	// RemoveAll with "directory not empty". PrunePhantomHosts fires
	// an audit event per prune which calls AsyncSave under the hood.
	t.Cleanup(func() { s.saveWG.Wait() })
	old := time.Now().Add(-phantomHostPruneAfterDefault - time.Hour)
	s.hosts["phantom"] = &models.Host{ID: "phantom", LastSeen: old}
	s.vms["phantom"] = []*models.VM{{UUID: "vm1", Name: "ghostly"}}
	s.storage["phantom"] = []*models.StoragePool{{Name: "default"}}
	s.hostOnlinePrev["phantom"] = false

	s.hosts["alive"] = &models.Host{ID: "alive", LastSeen: time.Now()}

	s.PrunePhantomHosts()

	s.mu.RLock()
	_, phantomStill := s.hosts["phantom"]
	_, aliveStill := s.hosts["alive"]
	_, vmsStill := s.vms["phantom"]
	_, storageStill := s.storage["phantom"]
	s.mu.RUnlock()

	if phantomStill {
		t.Fatal("expired host was not pruned")
	}
	if !aliveStill {
		t.Fatal("active host was pruned")
	}
	if vmsStill || storageStill {
		t.Fatal("pruned host left behind vms/storage entries")
	}
}

// TestPrunePhantomHosts_SkipsZeroLastSeen: a host with zero
// LastSeen (never reported) shouldn't be pruned — that's fresh
// state waiting for a first tick, not a phantom.
func TestPrunePhantomHosts_SkipsZeroLastSeen(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	s.hosts["fresh"] = &models.Host{ID: "fresh"} // LastSeen zero
	s.PrunePhantomHosts()
	s.mu.RLock()
	_, still := s.hosts["fresh"]
	s.mu.RUnlock()
	if !still {
		t.Fatal("fresh-never-seen host was pruned")
	}
}
