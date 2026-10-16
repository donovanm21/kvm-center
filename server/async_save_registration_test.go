package main

import (
	"context"
	"runtime"
	"sync"
	"testing"

	"kvm-center/internal/models"
)

type drainCaptureBackend struct {
	mu   sync.Mutex
	data *persistData
}

func (b *drainCaptureBackend) Name() string { return "drain-capture" }
func (b *drainCaptureBackend) Info(context.Context) BackendInfo {
	return BackendInfo{Type: b.Name(), Healthy: true}
}
func (b *drainCaptureBackend) Close(context.Context) error { return nil }
func (b *drainCaptureBackend) Load(context.Context) (*persistData, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data == nil {
		return &persistData{}, nil
	}
	return b.data, nil
}
func (b *drainCaptureBackend) Save(_ context.Context, data *persistData) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = data
	return nil
}

// A mutation must register its queued persistence before returning, so a drain
// cannot finish before the save even starts. One P makes an extra `go
// s.AsyncSave()` reproducibly leave the work unregistered when Wait is called.
func TestAsyncSaveMutationIsRegisteredBeforeReturn(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	for _, name := range []string{"enrollment", "unretire-exact", "unretire-short"} {
		t.Run(name, func(t *testing.T) {
			backend := &drainCaptureBackend{}
			s := NewStoreWithBackend(backend, t.TempDir()+"/unused.json")
			s.saveWG.Wait()
			defer func() {
				// Also drain the deliberately broken pre-fix implementation
				// when this regression is checked against old source.
				runtime.Gosched()
				s.saveWG.Wait()
			}()
			s.mu.Lock()
			s.pendingEnrollments = map[string]*models.PendingEnrollment{"e": {ID: "e", Status: "pending"}}
			host := "fixture.example.test"
			if name == "unretire-short" {
				host = "fixture"
			}
			s.removedHosts = map[string]*models.RemovedHost{host: {HostID: host}}
			s.mu.Unlock()
			s.Save()
			if name == "enrollment" {
				if !s.patchPendingEnrollment("e", func(e *models.PendingEnrollment) { e.Status = "rejected" }) {
					t.Fatal("fixture enrollment missing")
				}
			} else if !s.UnmarkHostRemoved("fixture.example.test") {
				t.Fatal("fixture retirement missing")
			}
			s.saveWG.Wait()
			data, err := backend.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if name == "enrollment" && data.PendingEnrollments["e"].Status != "rejected" {
				t.Fatal("drain returned before enrollment persistence was registered")
			}
			if name != "enrollment" && len(data.RemovedHosts) != 0 {
				t.Fatal("drain returned before unretire persistence was registered")
			}
		})
	}
}
