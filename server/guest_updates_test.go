package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"kvm-center/internal/models"
)

const updateVM1 = "11111111-1111-4111-8111-111111111111"
const updateVM2 = "22222222-2222-4222-8222-222222222222"

func updateStore(t *testing.T, backend Backend) *Store {
	t.Helper()
	if backend == nil {
		backend = NewJSONBackend(filepath.Join(t.TempDir(), "state.json"))
	}
	s := NewStoreWithBackend(backend, "")
	s.saveWG.Wait()
	s.mu.Lock()
	s.vms["fixture"] = []*models.VM{{UUID: updateVM1}, {UUID: updateVM2}}
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	return s
}
func queueUpdateTest(t *testing.T, s *Store, id, vm string) {
	t.Helper()
	if _, err := s.QueueGuestUpdate(id, []string{vm}); err != nil {
		t.Fatal(err)
	}
}
func TestGuestUpdateValidationAndImmutableReplay(t *testing.T) {
	s := updateStore(t, nil)
	for _, tc := range []struct {
		id      string
		targets []string
	}{{"", []string{updateVM1}}, {"id", nil}, {"id", []string{updateVM1, updateVM1}}, {"id", []string{"unknown"}}, {"id", []string{strings.ToUpper(updateVM1) + " "}}, {"id", []string{"33333333-3333-4333-8333-333333333333"}}} {
		if _, err := s.QueueGuestUpdate(tc.id, tc.targets); err == nil {
			t.Fatal("invalid admitted")
		}
	}
	if s.guestUpdates != nil {
		t.Fatal("validation mutated state")
	}
	queueUpdateTest(t, s, "req", updateVM1)
	if _, err := s.QueueGuestUpdate("req", []string{updateVM1}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, vm string }{{"req", updateVM2}, {"different", updateVM1}} {
		if _, err := s.QueueGuestUpdate(tc.id, []string{tc.vm}); err == nil {
			t.Fatal("binding/overlap accepted")
		}
	}
	for _, tc := range []struct{ os, arch, url string }{{"", "amd64", "https://example.invalid/binary"}, {"linux", "arm64", "https://example.invalid/binary"}, {"linux", "amd64", "file:///binary"}} {
		if _, offer, err := s.OfferGuestUpdate(updateVM1, tc.os, tc.arch, tc.url); err == nil || offer != "" {
			t.Fatal("unsupported offer")
		}
	}
	if !s.guestUpdateQueuedLocked(updateVM1) {
		t.Fatal("invalid platform consumed request")
	}
}
func TestGuestUpdateConcurrentOfferAndLostResponse(t *testing.T) {
	s := updateStore(t, nil)
	queueUpdateTest(t, s, "req", updateVM1)
	offers := make(chan string, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, offer, _ := s.OfferGuestUpdate(updateVM1, "linux", "amd64", "https://example.invalid/binary")
			offers <- offer
		}()
	}
	wg.Wait()
	close(offers)
	n := 0
	for offer := range offers {
		if offer != "" {
			n++
		}
	}
	if n != 1 {
		t.Fatal("duplicate offers", n)
	}
	// Discarding the first response never permits a second offer.
	_, offer, err := s.OfferGuestUpdate(updateVM1, "linux", "amd64", "https://example.invalid/changed")
	if err == nil || offer != "" {
		t.Fatal("lost response redispatched")
	}
	view, err := s.QueueGuestUpdate("req", []string{updateVM1})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view)
	if !bytes.Contains(raw, []byte("offered_unconfirmed")) || bytes.Contains(raw, []byte("https://")) {
		t.Fatal("replay exposed URL", string(raw))
	}
	queueUpdateTest(t, s, "other", updateVM2)
	if _, offer, err = s.OfferGuestUpdate(updateVM2, "windows", "amd64", "https://example.invalid/windows"); err != nil || offer == "" {
		t.Fatal("other target blocked", err)
	}
}
func TestGuestUpdateRestartAndStaleBackupFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := updateStore(t, NewJSONBackend(path))
	queueUpdateTest(t, s, "req", updateVM1)
	snapshot, err := s.ExportSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.OfferGuestUpdate(updateVM1, "linux", "amd64", "https://example.invalid/binary"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{snapshot, nil} {
		backend := NewJSONBackend(path)
		if raw != nil {
			restored := filepath.Join(t.TempDir(), "state.json")
			if err = os.WriteFile(restored, raw, 0600); err != nil {
				t.Fatal(err)
			}
			backend = NewJSONBackend(restored)
		}
		next := NewStoreWithBackend(backend, "")
		next.saveWG.Wait()
		if _, offer, err := next.OfferGuestUpdate(updateVM1, "linux", "amd64", "https://example.invalid/binary"); err == nil || offer != "" {
			t.Fatal("recovery redispatched")
		}
		view, err := next.QueueGuestUpdate("req", []string{updateVM1})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(view)
		want := "offered_unconfirmed"
		if raw != nil {
			want = "recovery_fenced"
		}
		if !bytes.Contains(data, []byte(want)) {
			t.Fatal(string(data))
		}
		queueUpdateTest(t, next, "new-target", updateVM2)
	}
}

type updateFailBackend struct {
	Backend
	fail, commit bool
}

func (b *updateFailBackend) Save(ctx context.Context, d *persistData) error {
	if !b.fail {
		return b.Backend.Save(ctx, d)
	}
	if b.commit {
		if err := b.Backend.Save(ctx, d); err != nil {
			return err
		}
	}
	return errors.New("synthetic uncertain save")
}
func TestGuestUpdateSaveUncertaintyNeverOffersOrExports(t *testing.T) {
	for _, phase := range []string{"queue", "offer"} {
		for _, commit := range []bool{false, true} {
			t.Run(phase+map[bool]string{true: "-commit", false: "-before"}[commit], func(t *testing.T) {
				b := &updateFailBackend{Backend: NewJSONBackend(filepath.Join(t.TempDir(), "state.json")), commit: commit}
				s := updateStore(t, b)
				if phase == "offer" {
					queueUpdateTest(t, s, "req", updateVM1)
				}
				b.fail = true
				if phase == "queue" {
					if _, err := s.QueueGuestUpdate("req", []string{updateVM1}); err == nil {
						t.Fatal("save error hidden")
					}
				} else {
					if _, offer, err := s.OfferGuestUpdate(updateVM1, "linux", "amd64", "https://example.invalid/binary"); err == nil || offer != "" {
						t.Fatal("uncertain offer emitted")
					}
				}
				if _, err := s.ExportSnapshot(); err == nil {
					t.Fatal("uncertain backup allowed")
				}
				b.fail = false
				if _, offer, err := s.OfferGuestUpdate(updateVM1, "linux", "amd64", "https://example.invalid/binary"); err == nil || offer != "" {
					t.Fatal("uncertainty regained ownership")
				}
			})
		}
	}
}
func TestGuestUpdateStrictHistoryAndNoErasure(t *testing.T) {
	s := updateStore(t, nil)
	queueUpdateTest(t, s, "req", updateVM1)
	raw, _ := json.Marshal(s.guestUpdates)
	for _, bad := range []string{`null`, `{"version":1,"requests":null}`, `{"version":1}`, strings.Replace(string(raw), `"requests"`, `"Requests"`, 1), strings.Replace(string(raw), `"phase":"queued"`, `"phase":null`, 1), strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1)} {
		var state guestUpdateState
		if json.Unmarshal([]byte(bad), &state) == nil {
			t.Fatal("malformed history accepted", bad)
		}
	}
	next := s.guestUpdates.clone()
	next.Requests = []guestUpdateRequest{}
	if guestUpdateTransition(s.guestUpdates, next) == nil {
		t.Fatal("history erased")
	}
	if err := s.backend.Save(context.Background(), &persistData{}); err == nil {
		t.Fatal("backend erased history")
	}
}
func TestGuestUpdateHTTPRefusalDoesNotConsumeReport(t *testing.T) {
	s := updateStore(t, nil)
	req := httptest.NewRequest("POST", "/api/kvm-tools/update", strings.NewReader(`{"uuids":["`+updateVM1+`"]}`))
	w := httptest.NewRecorder()
	handleKVMToolsUpdate(s)(w, req)
	if w.Code != 400 || s.guestUpdates != nil {
		t.Fatal("anonymous update admitted")
	}
	queueUpdateTest(t, s, "req", updateVM1)
	body := `{"vm_uuid":"` + updateVM1 + `","update_os":"linux","arch":"amd64","hostname":"synthetic"}`
	w = httptest.NewRecorder()
	handleGuestTelemetry(s)(w, httptest.NewRequest("POST", "https://example.invalid/api/guest-telemetry", strings.NewReader(body)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "update_url") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	handleGuestTelemetry(s)(w, httptest.NewRequest("POST", "https://example.invalid/api/guest-telemetry", strings.NewReader(body)))
	if w.Code != 200 || strings.Contains(w.Body.String(), "update_url") {
		t.Fatal("telemetry retry redispatched", w.Body.String())
	}
}

func TestGuestUpdateEncodedCapacityRoundTrip(t *testing.T) {
	state := &guestUpdateState{Version: 1, Requests: []guestUpdateRequest{}}
	for i := 0; i < 32; i++ {
		r := guestUpdateRequest{ID: fmt.Sprintf("request-%d", i), Version: "test", Revision: "test", Targets: []guestUpdateTarget{}}
		for j := 0; j < 32; j++ {
			r.Targets = append(r.Targets, guestUpdateTarget{UUID: fmt.Sprintf("%08x-1111-4111-8111-111111111111", i*32+j), Phase: "offered_unconfirmed", URL: "https://example.invalid/" + strings.Repeat("x", 800), OS: "linux", Arch: "amd64"})
		}
		state.Requests = append(state.Requests, r)
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var decoded guestUpdateState
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal("indented valid state refused", err)
	}
	b := NewJSONBackend(filepath.Join(t.TempDir(), "state.json"))
	if err = b.Save(context.Background(), &persistData{GuestUpdates: state}); err != nil {
		t.Fatal(err)
	}
	got, err := b.Load(context.Background())
	if err != nil || !reflect.DeepEqual(got.GuestUpdates, state) {
		t.Fatal("capacity round trip", err)
	}
	tooLarge := state.clone()
	for i := range tooLarge.Requests {
		for j := range tooLarge.Requests[i].Targets {
			tooLarge.Requests[i].Targets[j].URL += "" + strings.Repeat("x", 400)
		}
	}
	if tooLarge.Validate() == nil {
		t.Fatal("unreadable state accepted")
	}
	raw, _ = json.Marshal(tooLarge)
	if json.Unmarshal(raw, &decoded) == nil {
		t.Fatal("oversize compact state loaded")
	}
}

func TestGuestUpdateMigrationVerifiesRetainedLedger(t *testing.T) {
	s := updateStore(t, nil)
	queueUpdateTest(t, s, "migration", updateVM1)
	d := &persistData{GuestUpdates: s.guestUpdates.clone()}
	if err := verifyBackendRoundTrip(t.Context(), d, &stubBackend{stored: &persistData{}}); err == nil {
		t.Fatal("lost ledger accepted")
	}
	if err := verifyBackendRoundTrip(t.Context(), d, &stubBackend{stored: &persistData{GuestUpdates: d.GuestUpdates.clone()}}); err != nil {
		t.Fatal(err)
	}
}
