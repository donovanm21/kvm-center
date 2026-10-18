//go:build integration && historyacceptance && linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go.mongodb.org/mongo-driver/bson"
	"io"
	"kvm-center/internal/models"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryAcceptanceSessionLifecycle(t *testing.T) {
	binary := os.Getenv("KVMC_HISTORY_BINARY")
	if binary == "" {
		t.Fatal("current artifact required")
	}
	b := newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	root := t.TempDir()
	s, admin := acceptanceSeed(t, b, filepath.Join(root, "state.json"), false)
	password := "synthetic-session-password-only"
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.users["fixture-reader"].PasswordHash = hash
	s.users["fixture-disabled"] = &models.User{ID: "fixture-disabled", Enabled: false}
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		t.Fatal(err)
	}
	originalToken := "synthetic-original-session"
	original := sessionFixture(t, originalToken)
	originalPath := filepath.Join(root, "original-session.bson")
	originals := map[string][]byte{originalPath: original}
	if err := os.WriteFile(originalPath, original, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Collection(mcoSessions).InsertOne(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"renew", "expired", "orphan", "disabled"} {
		var doc bson.D
		bson.Unmarshal(sessionFixture(t, "synthetic-"+kind+"-session"), &doc)
		for i := range doc {
			switch {
			case doc[i].Key == "expiresat" && kind == "renew":
				doc[i].Value = time.Now().UTC().Add(time.Hour)
			case doc[i].Key == "expiresat" && kind == "expired":
				doc[i].Value = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			case doc[i].Key == "userid" && kind == "orphan":
				doc[i].Value = "missing"
			case doc[i].Key == "userid" && kind == "disabled":
				doc[i].Value = "fixture-disabled"
			}
		}
		raw, _ := bson.Marshal(doc)
		originalFile := filepath.Join(root, "original-"+kind+".bson")
		originals[originalFile] = raw
		if err := os.WriteFile(originalFile, raw, 0400); err != nil {
			t.Fatal(err)
		}
		if _, err := b.db.Collection(mcoSessions).InsertOne(t.Context(), doc); err != nil {
			t.Fatal(err)
		}
	}
	p := startAcceptance(t, binary, root, b, admin, false)
	request := func(method, path, token string, body []byte, want int) ([]byte, []*http.Cookie) {
		t.Helper()
		r, err := http.NewRequest(method, p.url+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		}
		res, err := p.client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if err != nil || res.StatusCode != want {
			t.Fatalf("session request %s %s status %d want %d: %v", method, path, res.StatusCode, want, err)
		}
		if token != "" && bytes.Contains(raw, []byte(token)) {
			t.Fatal("response body exposed session token")
		}
		return raw, res.Cookies()
	}
	request("POST", "/api/auth/login", "", []byte(`{"username":"fixture-reader","password":"invalid-synthetic"}`), 401)
	loginBody, _ := json.Marshal(map[string]string{"username": "fixture-reader", "password": password})
	_, cookies := request("POST", "/api/auth/login", "", loginBody, 200)
	minted := ""
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			minted = c.Value
			if !c.HttpOnly {
				t.Fatal("login cookie not HttpOnly")
			}
		}
	}
	if minted == "" {
		t.Fatal("login did not mint cookie")
	}
	request("GET", "/api/auth/me", minted, nil, 200)
	request("GET", "/api/auth/me", originalToken, nil, 200)
	_, cookies = request("GET", "/api/auth/me", "synthetic-renew-session", nil, 200)
	renewed := false
	for _, c := range cookies {
		if c.Name == sessionCookieName && time.Until(c.Expires) > sessionLifetime/2 {
			renewed = true
		}
	}
	if !renewed {
		t.Fatal("sliding cookie not renewed")
	}
	for _, kind := range []string{"expired", "orphan", "disabled"} {
		request("GET", "/api/auth/me", "synthetic-"+kind+"-session", nil, 401)
	}
	request("POST", "/api/auth/logout", minted, nil, 200)
	request("GET", "/api/auth/me", minted, nil, 401)
	p.stop(t, false)
	verify := func() {
		t.Helper()
		var got bson.Raw
		if err := b.db.Collection(mcoSessions).FindOne(t.Context(), bson.D{{Key: "_id", Value: originalToken}}).Decode(&got); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"token", "userid", "createdat", "useragent", "opaque"} {
			if !equalRaw(got.Lookup(key), original.Lookup(key)) {
				t.Fatal("retained session changed", key)
			}
		}
		if got.Lookup("expires_at").DateTime() != original.Lookup("expiresat").DateTime() {
			t.Fatal("expiry alias mapping changed value")
		}
		var renewed bson.Raw
		if err := b.db.Collection(mcoSessions).FindOne(t.Context(), bson.D{{Key: "_id", Value: "synthetic-renew-session"}}).Decode(&renewed); err != nil {
			t.Fatal(err)
		}
		if time.Until(time.UnixMilli(renewed.Lookup("expires_at").DateTime())) < sessionLifetime/2 || renewed.Lookup("opaque").Type == 0 {
			t.Fatal("renewal not retained")
		}
		for _, token := range []string{minted, "synthetic-expired-session"} {
			n, err := b.db.Collection(mcoSessions).CountDocuments(t.Context(), bson.D{{Key: "_id", Value: token}})
			if err != nil || n != 0 {
				t.Fatal("deleted session resurrected")
			}
		}
		for path, want := range originals {
			disk, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(disk, want) {
				t.Fatal("original changed")
			}
		}
	}
	verify()
	p = startAcceptance(t, binary, root, b, admin, false)
	request("GET", "/api/auth/me", originalToken, nil, 200)
	request("GET", "/api/auth/me", "synthetic-renew-session", nil, 200)
	request("GET", "/api/auth/me", minted, nil, 401)
	archive := p.request(t, "GET", "/api/admin/backup", nil, 200)
	p.stop(t, false)
	restoreRoot := t.TempDir()
	snapshot := unpackAcceptance(t, archive, restoreRoot, 0)
	if snapshot.EntityOverlay.Version != 8 || len(snapshot.Sessions) != 4 || snapshot.Sessions[originalToken] == nil {
		t.Fatal("session backup incomplete or deleted identity retained")
	}
	b = newMongoIntegrationBackend(t)
	requireInactiveGuestTTL(t, b)
	p = startAcceptance(t, binary, restoreRoot, b, admin, false)
	request("GET", "/api/auth/me", originalToken, nil, 200)
	request("GET", "/api/auth/me", "synthetic-renew-session", nil, 200)
	request("GET", "/api/auth/me", minted, nil, 401)
	verify()
	second := unpackAcceptance(t, p.request(t, "GET", "/api/admin/backup", nil, 200), t.TempDir(), 0)
	if !equalRaw(bson.Raw(second.EntityOverlay.Collections[mcoSessions][originalToken]).Lookup("opaque"), original.Lookup("opaque")) || second.Sessions[minted] != nil {
		t.Fatal("second backup lost session semantics")
	}
	p.stop(t, false)
	p = startAcceptance(t, binary, restoreRoot, b, admin, false)
	request("GET", "/api/auth/me", originalToken, nil, 200)
	request("GET", "/api/auth/me", minted, nil, 401)
	verify()
	p.stop(t, false)
	sum := sha256.Sum256(original)
	emitAcceptance(t, "session_lifecycle", map[string]any{"backend": "mongo", "fixture_schema": "synthetic-sessions-bson-v1", "original_sha256": hex.EncodeToString(sum[:]), "login_cookie_auth_renewal_logout_restart": true, "inactive_owner_expiry_refused": true, "deleted_sessions_not_resurrected": true, "application_backup_fresh_restore": true, "second_backup_restart": true, "opaque_preserved": true, "originals_unchanged": true, "ttl_monitor_disabled": true, "crash_durable_acknowledgement": false, "full_legacy_compatible": false})
}
