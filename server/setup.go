package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

// First-boot /setup wizard (v5.20). State piggybacks on the Features
// document so we don't introduce a second persistence path. The wizard
// is purely optional — every step has a safe default that guarantees
// the stack keeps working if the operator bails out at any point.

// GET /api/setup/state — public. The SPA polls this before rendering
// the layout so it can redirect an admin into the wizard on a fresh
// install. Returns {complete, completed_at, has_any_host}. has_any_host
// gives the wizard a cheap way to skip the "enrol a hypervisor" step
// if one is already checking in.
func handleSetupState(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		f := store.GetFeatures()
		writeJSON(w, http.StatusOK, map[string]any{
			"complete":     f.SetupComplete,
			"completed_at": f.SetupCompletedAt,
			"has_any_host": len(store.GetHosts()) > 0,
			"public_url":   f.PublicURL,
		})
	}
}

// POST /api/setup/complete — marks the wizard done so the SPA stops
// redirecting back here on every load. Requires user.manage (wrapped at
// route registration time in main.go) so a non-admin can't bypass the
// wizard for the operator.
func handleSetupComplete(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		f := store.GetFeatures()
		if !f.SetupComplete {
			f.SetupComplete = true
			f.SetupCompletedAt = time.Now().UTC()
			store.SetFeatures(f)
			audit(store, r, AuditFeatures, "setup", map[string]string{"complete": "true"}, true, "")
		}
		writeJSON(w, http.StatusOK, map[string]any{"complete": true})
	}
}

// handlePublicURL lets an admin set or clear the canonical public URL
// used by the Deployment page install snippets and notification deep
// links. POST body: {"public_url":"https://kvm.example.com"} — empty
// clears, falling back to KVM_CENTER_PUBLIC_URL env or the SPA's own
// window.location.
func handlePublicURL(store *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "POST / PUT only")
			return
		}
		var body struct {
			PublicURL string `json:"public_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		u := strings.TrimSpace(body.PublicURL)
		u = strings.TrimRight(u, "/")
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			writeError(w, http.StatusBadRequest, "public_url must start with http:// or https://")
			return
		}
		f := store.GetFeatures()
		f.PublicURL = u
		store.SetFeatures(f)
		audit(store, r, AuditFeatures, "public_url", map[string]string{"public_url": u}, true, "")
		writeJSON(w, http.StatusOK, map[string]any{"public_url": u})
	})
}

// seedPublicURLFromEnv copies KVM_CENTER_PUBLIC_URL into Features on
// first boot so bootstrap.sh --interactive can pre-populate the value
// without a UI round-trip. Once an admin saves any value (including
// empty) from the Settings page, the env is no longer consulted.
func seedPublicURLFromEnv(store *Store) {
	f := store.GetFeatures()
	if f.PublicURL != "" {
		return
	}
	if env := strings.TrimSpace(os.Getenv("KVM_CENTER_PUBLIC_URL")); env != "" {
		f.PublicURL = strings.TrimRight(env, "/")
		store.SetFeatures(f)
	}
}

// backfillSetupComplete marks the wizard complete on upgrade paths
// where the server already has users + features persisted. Runs once
// at boot. Without this every existing install would see the wizard
// on their next deploy, which would be surprising.
func backfillSetupComplete(store *Store) {
	f := store.GetFeatures()
	if f.SetupComplete {
		return
	}
	// "Existing install" heuristic: at least one admin account exists.
	// On a genuinely fresh boot there are no users until the first
	// /api/auth/register runs, so this leaves the wizard active for
	// first-boot operators while skipping it for anyone upgrading
	// in-place.
	for _, u := range store.ListUsers() {
		if u == nil {
			continue
		}
		for _, rid := range u.RoleIDs {
			if rid == "role_admin" {
				f.SetupComplete = true
				f.SetupCompletedAt = time.Now().UTC()
				store.SetFeatures(f)
				return
			}
		}
	}
}
