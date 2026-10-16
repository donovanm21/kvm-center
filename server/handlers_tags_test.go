package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func tagActor(s *Store) *models.User {
	u := &models.User{ID: "tag-operator", Enabled: true, RoleIDs: []string{"tag-role"}}
	s.mu.Lock()
	s.users[u.ID] = u
	s.roles["tag-role"] = &models.Role{ID: "tag-role", Permissions: []string{"vm.view", "vm.edit"}}
	s.vms = map[string][]*models.VM{"fixture": {{UUID: tagFixtureUUID, Name: "fixture-vm", HostID: "fixture"}}}
	s.mu.Unlock()
	return u
}
func tagRequest(s *Store, u *models.User, method, body string, scopes ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/vms/"+tagFixtureUUID+"/tags", strings.NewReader(body))
	if u != nil {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
	}
	if len(scopes) > 0 {
		r = r.WithContext(context.WithValue(r.Context(), tokenScopesCtxKey, scopes))
	}
	w := httptest.NewRecorder()
	vmDetailRoute(s).ServeHTTP(w, r)
	return w
}

const tagCreateBody = `{"tags":[" Production ","DATABASE","production"],"expected_tags":[]}`

func TestTagAPIWorkflowAndReload(t *testing.T) {
	s := newTestStore(t)
	u := tagActor(s)
	for _, body := range []string{tagCreateBody, tagCreateBody} {
		if w := tagRequest(s, u, "PUT", body); w.Code != 200 {
			t.Fatalf("save: %d %s", w.Code, w.Body)
		}
	}
	if !reflect.DeepEqual(s.GetVMTags(tagFixtureUUID), []string{"database", "production"}) {
		t.Fatal("normalization lost")
	}
	s.saveWG.Wait()
	loaded := NewStore(s.dataFile)
	t.Cleanup(func() { loaded.saveWG.Wait() })
	if w := tagRequest(loaded, u, "GET", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "production") {
		t.Fatalf("reload: %d %s", w.Code, w.Body)
	}
	if w := tagRequest(s, u, "PUT", `{"tags":["stale"],"expected_tags":[]}`); w.Code != 409 {
		t.Fatal("stale edit accepted")
	}
	if w := tagRequest(s, u, "PUT", `{"tags":["database"],"expected_tags":["database","production"]}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := tagRequest(s, u, "PUT", `{"tags":[],"expected_tags":["database"]}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	s.saveWG.Wait()
	cleared := NewStore(s.dataFile)
	t.Cleanup(func() { cleared.saveWG.Wait() })
	if len(cleared.GetVMTags(tagFixtureUUID)) != 0 {
		t.Fatal("clear resurrected")
	}
	events, _ := s.GetAuditEvents(AuditFilter{})
	count := 0
	for _, e := range events {
		if e.Action == "vm.tags.updated" {
			count++
			if e.ActorID != u.ID {
				t.Fatal("audit lost actor")
			}
		}
	}
	if count != 3 {
		t.Fatalf("audit count=%d", count)
	}
}
func TestTagAPIValidationAndAuthorization(t *testing.T) {
	s := newTestStore(t)
	u := tagActor(s)
	if w := tagRequest(s, nil, "GET", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	denied := &models.User{ID: "denied", Enabled: true}
	if w := tagRequest(s, denied, "PUT", tagCreateBody); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := tagRequest(s, u, "PUT", tagCreateBody, "vm.view"); w.Code != 403 {
		t.Fatal("read scope wrote tags")
	}
	if w := tagRequest(s, u, "GET", "", "vm.view"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, body := range []string{`{}`, `null`, `{"tags":[],"expected_tags":null}`, `{"tags":["bad tag"],"expected_tags":[]}`, `{"tags":[],"expected_tags":[],"extra":true}`, `{"tags":[],"tags":["x"],"expected_tags":[]}`, `{"Tags":[],"expected_tags":[]}`, tagCreateBody + ` {}`, strings.Repeat("x", 9000)} {
		if w := tagRequest(s, u, "PUT", body); w.Code != 400 {
			t.Fatalf("invalid %s: %d", body, w.Code)
		}
	}
	if len(s.GetVMTags(tagFixtureUUID)) != 0 {
		t.Fatal("invalid/denied request mutated")
	}
	if w := tagRequest(s, u, "DELETE", ""); w.Code != 405 {
		t.Fatal(w.Code)
	}
	s.mu.Lock()
	s.vms = nil
	s.mu.Unlock()
	if w := tagRequest(s, u, "PUT", tagCreateBody); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestTagAPISaveFailureRetryAndConflict(t *testing.T) {
	s := newTestStore(t)
	u := tagActor(s)
	s.saveWG.Wait()
	b := &resolutionBackend{Backend: s.backend}
	s.backend = b
	b.fail.Store(true)
	if w := tagRequest(s, u, "PUT", tagCreateBody); w.Code != 503 {
		t.Fatal(w.Code)
	}
	b.fail.Store(false)
	if w := tagRequest(s, u, "PUT", tagCreateBody); w.Code != 200 {
		t.Fatal(w.Body)
	}
	s.mu.Lock()
	s.vmTags = nil
	s.mu.Unlock()
	results := make(chan int, 2)
	for _, tag := range []string{"one", "two"} {
		go func(tag string) { results <- tagRequest(s, u, "PUT", `{"tags":["`+tag+`"],"expected_tags":[]}`).Code }(tag)
	}
	a, c := <-results, <-results
	if !((a == 200 && c == 409) || (a == 409 && c == 200)) {
		t.Fatalf("conflicting writes: %d %d", a, c)
	}
}
func TestTagAPIResponseOverlayAndCatalog(t *testing.T) {
	s := newTestStore(t)
	u := tagActor(s)
	if w := tagRequest(s, u, "PUT", tagCreateBody); w.Code != 200 {
		t.Fatal(w.Body)
	}
	w := httptest.NewRecorder()
	handleGetVMs(s).ServeHTTP(w, httptest.NewRequest("GET", "/api/vms?search=production", nil))
	var views []vmTagView
	if json.Unmarshal(w.Body.Bytes(), &views) != nil || len(views) != 1 || len(views[0].Tags) != 2 {
		t.Fatal(w.Body)
	}
	views[0].Tags[0] = "changed"
	views[0].Name = "changed"
	if s.GetVMTags(tagFixtureUUID)[0] != "database" || s.GetVMByUUID(tagFixtureUUID).Name != "fixture-vm" {
		t.Fatal("response aliased Store")
	}
	r := httptest.NewRequest("GET", "/api/tags", nil)
	r = r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
	w = httptest.NewRecorder()
	requirePerm(s, "vm.view", handleTags(s)).ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"count":1`) {
		t.Fatal(w.Body)
	}
}

func TestTagAPITelemetryAndAuthenticatedScopes(t *testing.T) {
	s := newTestStore(t)
	u := tagActor(s)
	token, _, err := s.CreateAPIToken("tag-read", u.ID, "user", time.Time{}, []string{"vm.view"})
	if err != nil {
		t.Fatal(err)
	}
	for method, want := range map[string]int{"GET": 200, "PUT": 403} {
		r := httptest.NewRequest(method, "/api/vms/"+tagFixtureUUID+"/tags", strings.NewReader(tagCreateBody))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		authMiddleware(s, vmDetailRoute(s)).ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("authenticated %s: %d", method, w.Code)
		}
	}
	if w := tagRequest(s, u, "PUT", tagCreateBody); w.Code != 200 {
		t.Fatal(w.Body)
	}
	s.ApplyTelemetry(&models.TelemetryPayload{HostID: "fixture"})
	if len(s.GetVMTags(tagFixtureUUID)) != 2 {
		t.Fatal("empty telemetry pruned tags")
	}
	s.ApplyTelemetry(&models.TelemetryPayload{HostID: "new-fixture", VMs: []models.VM{{UUID: tagFixtureUUID, Name: "renamed"}}})
	view := tagView(s, s.GetVMByUUID(tagFixtureUUID))
	if view.Name != "renamed" || view.HostID != "new-fixture" || len(view.Tags) != 2 {
		t.Fatal("identity continuity lost")
	}
}
func TestTagAPIAcknowledgementWaitsAndLoadGate(t *testing.T) {
	s := newTestStore(t)
	u := tagActor(s)
	s.saveWG.Wait()
	b := &resolutionBackend{Backend: s.backend, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.backend = b
	done := make(chan int, 1)
	go func() { done <- tagRequest(s, u, "PUT", tagCreateBody).Code }()
	<-b.entered
	select {
	case code := <-done:
		t.Fatalf("premature acknowledgement: %d", code)
	default:
	}
	close(b.release)
	if code := <-done; code != 200 {
		t.Fatal(code)
	}
	s.saveWG.Wait()
	s.mu.Lock()
	s.saveGateOpen = false
	s.mu.Unlock()
	if w := tagRequest(s, u, "PUT", `{"tags":[],"expected_tags":["database","production"]}`); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if len(s.GetVMTags(tagFixtureUUID)) != 2 {
		t.Fatal("load-gated mutation staged")
	}
}
