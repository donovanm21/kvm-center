package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kvm-center/internal/models"
)

// First-boot setup wizard (v5.20). Covers:
//
//   - /api/setup/state reports complete=false on a fresh store and
//     complete=true once the flag flips.
//   - /api/setup/complete sets both the flag and the timestamp and
//     is idempotent on a second call.
//   - backfillSetupComplete auto-marks complete when an admin user
//     already exists (upgrade-path invariant), and leaves it false
//     when no admin exists (genuine first boot).
//   - /api/settings/update preserves SetupComplete even when the
//     payload omits it — the v5.20.3 fix that prevents a stale SPA
//     tab from retriggering the wizard.

func TestSetup_StateBeforeAndAfterComplete(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")

	// Initial state — fresh store, no admin registered yet.
	rr := httptest.NewRecorder()
	handleSetupState(s)(rr, httptest.NewRequest("GET", "/api/setup/state", nil))
	if rr.Code != 200 {
		t.Fatalf("state: got %d, want 200", rr.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["complete"] != false {
		t.Errorf("expected complete=false on fresh store, got %v", got["complete"])
	}
	if got["has_any_host"] != false {
		t.Errorf("expected has_any_host=false with zero hosts, got %v", got["has_any_host"])
	}

	// Flip complete via the handler.
	rr = httptest.NewRecorder()
	handleSetupComplete(s)(rr, httptest.NewRequest("POST", "/api/setup/complete", nil))
	if rr.Code != 200 {
		t.Fatalf("complete: got %d, want 200", rr.Code)
	}

	f := s.GetFeatures()
	if !f.SetupComplete {
		t.Error("expected SetupComplete=true after /complete")
	}
	if f.SetupCompletedAt.IsZero() {
		t.Error("expected SetupCompletedAt to be stamped after /complete")
	}

	// State now reports complete.
	rr = httptest.NewRecorder()
	handleSetupState(s)(rr, httptest.NewRequest("GET", "/api/setup/state", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got["complete"] != true {
		t.Errorf("expected complete=true after flip, got %v", got["complete"])
	}
}

func TestSetup_CompleteIsIdempotent(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")

	rr := httptest.NewRecorder()
	handleSetupComplete(s)(rr, httptest.NewRequest("POST", "/api/setup/complete", nil))
	first := s.GetFeatures().SetupCompletedAt

	// Second call must not re-stamp — an operator flipping through
	// the wizard twice shouldn't churn the timestamp.
	rr = httptest.NewRecorder()
	handleSetupComplete(s)(rr, httptest.NewRequest("POST", "/api/setup/complete", nil))
	second := s.GetFeatures().SetupCompletedAt

	if !first.Equal(second) {
		t.Errorf("SetupCompletedAt churned on repeat call: %v -> %v", first, second)
	}
}

func TestSetup_RejectsNonPOST(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		rr := httptest.NewRecorder()
		handleSetupComplete(s)(rr, httptest.NewRequest(m, "/api/setup/complete", nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: got %d, want 405", m, rr.Code)
		}
	}
}

func TestBackfill_SkipsFreshInstall(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	// No admin user → wizard stays active.
	backfillSetupComplete(s)
	if s.GetFeatures().SetupComplete {
		t.Error("backfill should not mark complete on a fresh install")
	}
}

func TestBackfill_MarksCompleteWhenAdminExists(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	// Plant a pre-existing admin user — what an upgrade-path install
	// looks like from the wizard's perspective.
	if err := s.CreateUser(&models.User{
		ID:       "user_test_admin",
		Username: "existing",
		Enabled:  true,
		RoleIDs:  []string{"role_admin"},
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	backfillSetupComplete(s)
	if !s.GetFeatures().SetupComplete {
		t.Error("backfill should mark complete when an admin already exists")
	}
}

func TestFeaturesUpdate_PreservesSetupComplete(t *testing.T) {
	// Regression test for v5.20.3: POST /api/settings/update decodes
	// a full Features struct; an older SPA tab would send a payload
	// missing setup_complete and the server would silently zero it,
	// retriggering the wizard. Preservation is handled inside the
	// handler; this test locks it in.
	s := NewStore(t.TempDir() + "/data.json")
	f := s.GetFeatures()
	f.SetupComplete = true
	s.SetFeatures(f)

	// Payload WITHOUT setup_complete (simulates the stale tab).
	body := []byte(`{"disable_vm_create": true}`)
	req := httptest.NewRequest("POST", "/api/settings/update", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handleFeaturesUpdate(s).ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("update: got %d, body=%s", rr.Code, rr.Body.String())
	}

	got := s.GetFeatures()
	if !got.SetupComplete {
		t.Error("SetupComplete should survive a payload that omits it")
	}
	if !got.DisableVMCreate {
		t.Error("Disable* toggles in the payload should still apply")
	}
}
