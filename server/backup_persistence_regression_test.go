package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kvm-center/internal/models"
)

type retrySaveBackend struct {
	stubBackend
	failNext bool
	attempts []*persistData
}

func (b *retrySaveBackend) Save(ctx context.Context, d *persistData) error {
	b.attempts = append(b.attempts, d)
	if b.failNext {
		b.failNext = false
		return errors.New("injected write failure")
	}
	return b.stubBackend.Save(ctx, d)
}
func persistenceTestStore(t *testing.T) (*Store, *retrySaveBackend) {
	t.Helper()
	b := &retrySaveBackend{stubBackend: stubBackend{name: "test"}}
	s := NewStoreWithBackend(b, filepath.Join(t.TempDir(), "data.json"))
	s.saveWG.Wait()
	t.Cleanup(func() { s.saveWG.Wait() })
	s.Save()
	b.attempts = nil
	return s, b
}
func TestPersistence_FailedSaveRetainsDirtyCollections(t *testing.T) {
	s, b := persistenceTestStore(t)
	s.mu.Lock()
	s.roles["new-role"] = &models.Role{ID: "new-role", Name: "changed"}
	s.mu.Unlock()
	b.failNext = true
	s.Save()
	s.Save()
	if len(b.attempts) != 2 {
		t.Fatalf("save attempts=%d", len(b.attempts))
	}
	for i, d := range b.attempts {
		if !isDirty(d, "roles") {
			t.Errorf("attempt %d skipped changed roles", i+1)
		}
	}
	s.Save()
	if isDirty(b.attempts[2], "roles") {
		t.Error("successful unchanged save should clear roles dirty bit")
	}
}
func TestPersistence_BackupDoesNotConsumeDirtyCollections(t *testing.T) {
	s, b := persistenceTestStore(t)
	s.mu.Lock()
	s.roles["backup-role"] = &models.Role{ID: "backup-role", Name: "changed"}
	s.mu.Unlock()
	if _, err := s.ExportSnapshot(); err != nil {
		t.Fatal(err)
	}
	s.Save()
	if !isDirty(b.attempts[0], "roles") {
		t.Fatal("backup suppressed pending roles save")
	}
}
func TestPersistence_DirtyCollectionsArePerStore(t *testing.T) {
	a, ba := persistenceTestStore(t)
	b, bb := persistenceTestStore(t)
	for _, s := range []*Store{a, b} {
		s.mu.Lock()
		s.roles["shared"] = &models.Role{ID: "shared", Name: "same change"}
		s.mu.Unlock()
	}
	a.Save()
	b.Save()
	if !isDirty(ba.attempts[0], "roles") || !isDirty(bb.attempts[0], "roles") {
		t.Fatal("one store consumed another store's dirty collection")
	}
}
func TestBackup_PreservesAllStateAndPrivateFields(t *testing.T) {
	s, _ := persistenceTestStore(t)
	expiry := time.Now().UTC().Add(time.Hour)
	s.mu.Lock()
	s.discordGateway = &models.DiscordGateway{Token: "test-secret", Host: "test.local"}
	s.removedHosts = map[string]*models.RemovedHost{"removed": {HostID: "removed"}}
	s.pendingEnrollments = map[string]*models.PendingEnrollment{"pending": {ID: "pending", PollTokenHash: "test-hash", IssuedCertPEM: "test-cert", IssuedCAPEM: "test-ca"}}
	s.mu.Unlock()
	s.commandLog.Record(&models.CommandLogEntry{RequestID: "request", Action: "START", ExpiresAt: &expiry})
	raw, err := s.ExportSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var d persistData
	if err = json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.DiscordGateway == nil || d.DiscordGateway.Token != "test-secret" {
		t.Error("gateway pairing missing from backup")
	}
	if d.RemovedHosts["removed"] == nil {
		t.Error("removed hosts missing from backup")
	}
	if p := d.PendingEnrollments["pending"]; p == nil || p.PollTokenHash != "test-hash" || p.IssuedCertPEM != "test-cert" || p.IssuedCAPEM != "test-ca" {
		t.Error("pending enrollment state missing from backup")
	}
	if c := d.CommandLog["request"]; c == nil || c.ExpiresAt == nil || !c.ExpiresAt.Equal(expiry) {
		t.Error("command history or expiration missing from backup")
	}
	// Persisting a snapshot through JSON must retain the same private state.
	dst := NewJSONBackend(filepath.Join(t.TempDir(), "restored.json"))
	if err := dst.Save(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	back, err := dst.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if back.DiscordGateway == nil || back.DiscordGateway.Token != "test-secret" {
		t.Error("JSON persistence discarded gateway pairing")
	}
	// Model API encoding must still redact private fields.
	api, _ := json.Marshal(s.discordGateway)
	var exposed map[string]json.RawMessage
	_ = json.Unmarshal(api, &exposed)
	if _, ok := exposed["token"]; ok {
		t.Error("private token exposed by API model")
	}
}
func TestBackup_ConfiguredMaterialReadFailureIsFatal(t *testing.T) {
	s, _ := persistenceTestStore(t)
	old := serverBackupPaths
	defer func() { serverBackupPaths = old }()
	serverBackupPaths = backupPaths{TLSKey: filepath.Join(t.TempDir(), "missing.key")}
	rr := httptest.NewRecorder()
	handleBackup(s)(rr, httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil))
	s.saveWG.Wait()
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("incomplete backup status=%d", rr.Code)
	}
	if _, err := s.runScheduledBackup(&models.BackupSchedule{DestPath: t.TempDir()}, time.Now()); err == nil {
		t.Fatal("scheduled backup succeeded without configured key")
	}
}
func TestRestore_FailsClosedWithoutChangingState(t *testing.T) {
	s, _ := persistenceTestStore(t)
	path := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(path, []byte("existing-state"), 0600); err != nil {
		t.Fatal(err)
	}
	old := serverRestorePaths
	defer func() { serverRestorePaths = old }()
	serverRestorePaths = restorePaths{DataFile: path}
	rr := httptest.NewRecorder()
	handleRestore(s)(rr, httptest.NewRequest(http.MethodPost, "/api/admin/restore", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("restore status=%d, want 503", rr.Code)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "existing-state" {
		t.Fatal("restore changed live data")
	}
}

func TestPersistence_PrivateOnlyChangeIsDirty(t *testing.T) {
	s, b := persistenceTestStore(t)
	s.mu.Lock()
	s.discordGateway = &models.DiscordGateway{Host: "test.local", Token: "first"}
	s.mu.Unlock()
	s.Save()
	b.attempts = nil
	s.mu.Lock()
	s.discordGateway.Token = "rotated"
	s.mu.Unlock()
	s.Save()
	if !isDirty(b.attempts[0], "discord_gateway") {
		t.Fatal("private-only pairing change was skipped")
	}
	if b.stored.DiscordGateway.Token != "rotated" {
		t.Fatal("rotated token was not persisted")
	}
}

func TestPersistence_LegacyJSONStillLoads(t *testing.T) {
	var d persistData
	if err := json.Unmarshal([]byte(`{"hosts":{},"discord_gateway":{"host":"legacy.local"},"pending_enrollments":{"one":{"id":"one"}}}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.DiscordGateway == nil || d.DiscordGateway.Host != "legacy.local" || d.PendingEnrollments["one"] == nil {
		t.Fatal("legacy state was not decoded")
	}
	if d.DiscordGateway.Token != "" {
		t.Fatal("legacy input acquired a credential")
	}
}

func TestJSONBackend_PrivateStateFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	// Existing temporary files must not preserve permissive mode bits.
	if err := os.WriteFile(path+".tmp", []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := NewJSONBackend(path).Save(context.Background(), &persistData{DiscordGateway: &models.DiscordGateway{Token: "private"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode=%#o, want 0600", got)
	}
}
