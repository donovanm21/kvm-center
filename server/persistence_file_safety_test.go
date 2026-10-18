package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func TestPersistenceFiles_UsePrivateExclusiveTemporaryFiles(t *testing.T) {
	for _, writer := range []string{"json", "scheduled"} {
		for _, kind := range []string{"regular", "symlink", "hardlink"} {
			t.Run(writer+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				ts := time.Date(2026, 10, 16, 7, 0, 0, 0, time.UTC)
				name := "data.json"
				if writer == "scheduled" {
					name = "kvm-center-backup-" + ts.Format("20060102-150405") + ".tar.gz"
				}
				full := filepath.Join(dir, name)
				legacyTmp := full + ".tmp"
				target := legacyTmp
				if kind != "regular" {
					target = filepath.Join(dir, "unrelated-file")
				}
				const original = "must remain untouched"
				if err := os.WriteFile(target, []byte(original), 0644); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(target, legacyTmp); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "hardlink" {
					if err := os.Link(target, legacyTmp); err != nil {
						t.Fatal(err)
					}
				}

				if writer == "json" {
					if err := NewJSONBackend(full).Save(context.Background(), &persistData{ConsoleSecret: "test-private-data"}); err != nil {
						t.Fatal(err)
					}
				} else {
					old := serverBackupPaths
					serverBackupPaths = backupPaths{}
					defer func() { serverBackupPaths = old }()
					s := &Store{saveGateOpen: true, commandLog: NewCommandLog()}
					if _, err := s.runScheduledBackup(&models.BackupSchedule{DestPath: dir}, ts); err != nil {
						t.Fatal(err)
					}
				}
				info, err := os.Stat(full)
				if err != nil {
					t.Fatal(err)
				}
				if got := info.Mode().Perm(); got != 0600 {
					t.Errorf("published file permissions=%#o, want 0600", got)
				}
				for _, path := range []string{target, legacyTmp} {
					data, err := os.ReadFile(path)
					if err != nil || string(data) != original {
						t.Errorf("pre-existing file %s changed: %q, %v", path, data, err)
					}
				}
				assertNoPrivateTemps(t, dir, name)
			})
		}
	}
}

func TestPersistenceFiles_FailedRenameCleansTemporaryFiles(t *testing.T) {
	for _, writer := range []string{"json", "scheduled"} {
		t.Run(writer, func(t *testing.T) {
			dir := t.TempDir()
			ts := time.Date(2026, 10, 16, 7, 0, 0, 0, time.UTC)
			name := "data.json"
			if writer == "scheduled" {
				name = "kvm-center-backup-" + ts.Format("20060102-150405") + ".tar.gz"
			}
			full := filepath.Join(dir, name)
			if err := os.Mkdir(full, 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			if writer == "json" {
				err = NewJSONBackend(full).Save(context.Background(), &persistData{})
			} else {
				old := serverBackupPaths
				serverBackupPaths = backupPaths{}
				defer func() { serverBackupPaths = old }()
				s := &Store{saveGateOpen: true, commandLog: NewCommandLog()}
				_, err = s.runScheduledBackup(&models.BackupSchedule{DestPath: dir}, ts)
			}
			if err == nil {
				t.Fatal("expected rename onto directory to fail")
			}
			if info, statErr := os.Stat(full); statErr != nil || !info.IsDir() {
				t.Fatalf("original destination changed: %v", statErr)
			}
			assertNoPrivateTemps(t, dir, name)
		})
	}
}

func assertNoPrivateTemps(t *testing.T, dir, name string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "."+name+".") && strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temporary file leaked: %s", entry.Name())
		}
	}
}
