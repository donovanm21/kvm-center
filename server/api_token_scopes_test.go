package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// Token-scope tests (v5.51). Scopes are an additional constraint on
// top of owner permissions: a scoped token can never grant a perm the
// owner doesn't already hold, and an empty scope list preserves the
// pre-v5.51 "inherit owner perms" behaviour.

func TestValidateScopes_Cleanup(t *testing.T) {
	// Empty / nil pass through as nil (distinct from "explicit empty").
	if got, err := validateScopes(nil); err != nil || got != nil {
		t.Errorf("nil input: got %v err %v", got, err)
	}
	if got, err := validateScopes([]string{}); err != nil || got != nil {
		t.Errorf("empty input: got %v err %v", got, err)
	}
	// Dedup, sort, trim, drop empty entries.
	got, err := validateScopes([]string{"vm.view", "  vm.view  ", "", "audit.view"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"audit.view", "vm.view"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cleanup: got %v want %v", got, want)
	}
}

func TestValidateScopes_RejectsUnknown(t *testing.T) {
	// Typo at mint time would otherwise produce a token whose scope
	// never matches a real perm — silent over-grant. Reject loudly.
	_, err := validateScopes([]string{"vm.view", "vm.megabytes"})
	if err == nil {
		t.Fatal("expected error on unknown scope")
	}
}

func TestCreateAPIToken_ScopesPersisted(t *testing.T) {
	s := newTestStore(t)
	_, rec, err := s.CreateAPIToken("ci", "u1", "user", time.Time{}, []string{"vm.view", "audit.view"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Scopes) != 2 {
		t.Fatalf("scopes not persisted: %v", rec.Scopes)
	}
	stored := s.GetAPIToken(rec.ID)
	if !reflect.DeepEqual(stored.Scopes, []string{"audit.view", "vm.view"}) {
		t.Errorf("stored scopes: got %v", stored.Scopes)
	}
}

func TestAPIToken_AllowsPermission(t *testing.T) {
	cases := []struct {
		name   string
		scopes []string
		check  string
		want   bool
	}{
		{"legacy nil scopes inherit", nil, "vm.delete", true},
		{"legacy empty scopes inherit", []string{}, "vm.delete", true},
		{"scope match", []string{"vm.view"}, "vm.view", true},
		{"scope miss", []string{"vm.view"}, "vm.delete", false},
		{"admin wildcard grants all", []string{"admin"}, "vm.delete", true},
		{"admin wildcard with siblings", []string{"vm.view", "admin"}, "vm.migrate", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok := &models.APIToken{Scopes: c.scopes}
			if got := tok.AllowsPermission(c.check); got != c.want {
				t.Errorf("AllowsPermission(%q) with scopes=%v: got %v want %v",
					c.check, c.scopes, got, c.want)
			}
		})
	}
}

// requirePerm gate: scoped tokens are an extra fence. Owner must
// still hold the perm; scope must include it. Either failing → 403.
func TestRequirePerm_TokenScopeGate(t *testing.T) {
	s := newTestStore(t)
	// Give u1 a role that grants both vm.view and vm.delete.
	s.mu.Lock()
	s.roles["r-broad"] = &models.Role{ID: "r-broad", Name: "broad", Permissions: []string{"vm.view", "vm.delete"}}
	s.users["u1"].RoleIDs = []string{"r-broad"}
	s.mu.Unlock()

	type tok struct {
		name   string
		scopes []string
	}
	mintToken := func(scopes []string) string {
		full, _, err := s.CreateAPIToken("t-"+time.Now().Format("150405.000000"), "u1", "user", time.Time{}, scopes)
		if err != nil {
			t.Fatal(err)
		}
		return full
	}

	// Build a tiny mux with just /api/vms (vm.view) so we can hit
	// authMiddleware → requirePerm end-to-end via Bearer auth.
	mux := http.NewServeMux()
	called := false
	mux.Handle("/api/probe", requirePerm(s, "vm.delete", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})))
	srv := httptest.NewServer(authMiddleware(s, mux))
	defer srv.Close()

	hit := func(t *testing.T, bearer string, wantStatus int) {
		t.Helper()
		called = false
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/probe", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != wantStatus {
			t.Errorf("status: got %d want %d", resp.StatusCode, wantStatus)
		}
	}

	t.Run("legacy unscoped token inherits owner perms", func(t *testing.T) {
		hit(t, mintToken(nil), http.StatusOK)
		if !called {
			t.Error("handler not reached")
		}
	})
	t.Run("scoped token narrows — denies missing perm", func(t *testing.T) {
		hit(t, mintToken([]string{"vm.view"}), http.StatusForbidden)
		if called {
			t.Error("handler reached despite scope mismatch")
		}
	})
	t.Run("scoped token allows when scope matches", func(t *testing.T) {
		hit(t, mintToken([]string{"vm.delete"}), http.StatusOK)
	})
	t.Run("admin wildcard scope is permissive", func(t *testing.T) {
		hit(t, mintToken([]string{"admin"}), http.StatusOK)
	})
	t.Run("scope cannot elevate beyond owner perms", func(t *testing.T) {
		// Owner has no host.manage role, scope grants it — should still 403.
		mux2 := http.NewServeMux()
		mux2.Handle("/api/probe", requirePerm(s, "host.manage", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })))
		srv2 := httptest.NewServer(authMiddleware(s, mux2))
		defer srv2.Close()

		full, _, err := s.CreateAPIToken("elev", "u1", "user", time.Time{}, []string{"host.manage"})
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodGet, srv2.URL+"/api/probe", nil)
		req.Header.Set("Authorization", "Bearer "+full)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("scope-elevation should 403; got %d", resp.StatusCode)
		}
	})
}
