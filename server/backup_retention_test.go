package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneScheduledBackups_HandlesEncryptedAndPlain(t *testing.T) {
	dir := t.TempDir()
	valid := []string{
		"kvm-center-backup-20261016-010000.tar.gz",
		"kvm-center-backup-20261016-020000.tar.gz.enc",
		"kvm-center-backup-20261016-030000.tar.gz",
		"kvm-center-backup-20261016-040000.tar.gz.enc",
	}
	for i, name := range valid {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		at := time.Unix(int64(i+1), 0)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneScheduledBackups(dir, 2); err != nil {
		t.Fatal(err)
	}
	for i, name := range valid {
		_, err := os.Stat(filepath.Join(dir, name))
		if i < 2 && !os.IsNotExist(err) {
			t.Errorf("old archive %s was not pruned: %v", name, err)
		}
		if i >= 2 && err != nil {
			t.Errorf("new archive %s was pruned: %v", name, err)
		}
	}
}

func TestPruneScheduledBackups_PreservesUnrelatedFilesAndLinks(t *testing.T) {
	dir := t.TempDir()
	preserved := []string{
		"notes.tar.gz",
		"kvm-center-backup-manual.tar.gz",
		"kvm-center-backup-20261399-250000.tar.gz.enc",
		"kvm-center-backup-20261016-010000.tar.gz.tmp",
		"kvm-center-backup-20261016-010000.tar.gz.enc.tmp",
		".kvm-center-backup-20261016-010000.tar.gz.123.tmp",
	}
	for _, name := range preserved {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	folder := "kvm-center-backup-20261016-020000.tar.gz"
	if err := os.Mkdir(filepath.Join(dir, folder), 0700); err != nil {
		t.Fatal(err)
	}
	link := "kvm-center-backup-20261016-030000.tar.gz"
	if err := os.Symlink(filepath.Join(dir, preserved[0]), filepath.Join(dir, link)); err != nil {
		t.Fatal(err)
	}
	newest := "kvm-center-backup-20261016-040000.tar.gz"
	if err := os.WriteFile(filepath.Join(dir, newest), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := pruneScheduledBackups(dir, 1); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(preserved, folder, link, newest) {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Errorf("unrelated path %s removed: %v", name, err)
		}
	}
}

func TestPruneScheduledBackups_DisabledRetentionPreservesAll(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "kvm-center-backup-20261016-010000.tar.gz.enc")
	if err := os.WriteFile(name, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := pruneScheduledBackups(dir, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); err != nil {
		t.Fatal(err)
	}
}
