package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Backend abstracts the kvm-center persistence layer so the same
// in-memory Store can be backed by either the legacy JSON file
// (JSONBackend) or MongoDB (MongoBackend). The Store hydrates itself
// from backend.Load() at startup and calls backend.Save() on its
// periodic persist tick + after RBAC mutations. Backends are swappable
// at runtime via the Settings → Database UI (see migrateBackend).
//
// The interface intentionally trades efficiency for simplicity in
// v5.1.0: a Save() ships the entire persistData payload through the
// backend. For today's fleet sizes this is cheap and keeps the
// Mongo adapter small; we'll evolve to per-collection deltas if
// telemetry volume grows enough to make it matter.
type Backend interface {
	// Name returns a short identifier ("json" | "mongo") for logs + UI.
	Name() string

	// Info returns operator-facing metadata: connection string, size,
	// last modification timestamp. Creds are redacted. Used by the
	// Settings → Database "current backend" card.
	Info(ctx context.Context) BackendInfo

	// Load hydrates the in-memory store at startup. Returning an empty
	// *persistData with a nil error is legitimate (fresh install).
	Load(ctx context.Context) (*persistData, error)

	// Save atomically commits the entire in-memory state. Runs on the
	// 30-second persist ticker and after RBAC mutations.
	Save(ctx context.Context, d *persistData) error

	// Close releases any long-lived connections. Idempotent.
	Close(ctx context.Context) error
}

// BackendInfo is what Settings → Database renders.
type BackendInfo struct {
	Type         string `json:"type"`                    // "json" | "mongo"
	Endpoint     string `json:"endpoint,omitempty"`      // file path or mongodb://host/db (no creds)
	SizeBytes    int64  `json:"size_bytes,omitempty"`    // data.json size or Mongo dbStats dataSize
	LastModified string `json:"last_modified,omitempty"` // RFC3339
	Healthy      bool   `json:"healthy"`
	Error        string `json:"error,omitempty"`
}

// JSONBackend keeps kvm-center on the pre-v5.1 single-file store.
// Preserves the exact on-disk format so downgrading to v5.0.x
// boot-straps without a migration.
type JSONBackend struct {
	Path string
}

func NewJSONBackend(path string) *JSONBackend { return &JSONBackend{Path: path} }

func (b *JSONBackend) Name() string { return "json" }

func (b *JSONBackend) Info(_ context.Context) BackendInfo {
	info := BackendInfo{Type: "json", Endpoint: b.Path, Healthy: true}
	fi, err := os.Stat(b.Path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			info.Healthy = false
			info.Error = err.Error()
		}
		return info
	}
	info.SizeBytes = fi.Size()
	info.LastModified = fi.ModTime().UTC().Format(time.RFC3339)
	return info
}

func (b *JSONBackend) Load(_ context.Context) (*persistData, error) {
	raw, err := os.ReadFile(b.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &persistData{}, nil
		}
		return nil, err
	}
	var d persistData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("json backend: unmarshal %s: %w", b.Path, err)
	}
	return &d, nil
}

func (b *JSONBackend) Save(_ context.Context, d *persistData) error {
	if err := os.MkdirAll(filepath.Dir(b.Path), 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	// Write to a sibling temp, fsync, then rename so an interrupted
	// Save never leaves the data file half-written and a power loss
	// after the rename still finds the bytes durably on disk.
	tmp := b.Path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, b.Path)
}

func (b *JSONBackend) Close(_ context.Context) error { return nil }

// migrateBackend copies every persisted entity from src to dst in a
// single shot. Used both for the one-time "first boot after Mongo is
// configured" migration and the Settings → Database "Switch backend"
// operator action.
//
// Post-Save verification (Tier S #14, v5.32.19): after the dst Save
// completes, read back through dst.Load and assert key collection
// counts match the source. If the round-trip loses rows, refuse to
// declare success — the caller keeps using src rather than rebinding
// the live store to a partially-populated backend.
func migrateBackend(ctx context.Context, src, dst Backend) error {
	if src == nil || dst == nil {
		return errors.New("source and destination backends are both required")
	}
	log.Printf("backend-migrate: %s → %s", src.Name(), dst.Name())
	d, err := src.Load(ctx)
	if err != nil {
		return fmt.Errorf("load from %s: %w", src.Name(), err)
	}
	if d == nil {
		d = &persistData{}
	}
	if err := dst.Save(ctx, d); err != nil {
		return fmt.Errorf("save to %s: %w", dst.Name(), err)
	}
	if err := verifyBackendRoundTrip(ctx, d, dst); err != nil {
		return fmt.Errorf("round-trip verify on %s: %w", dst.Name(), err)
	}
	log.Printf("backend-migrate: %s → %s OK (round-trip verified)", src.Name(), dst.Name())
	return nil
}

// verifyBackendRoundTrip reads the destination back and checks that
// the critical collections (hosts, users, hypervisor_certs,
// migrations, api_tokens, snapshot_policies) match the source counts.
// Missing rows in any of those is a data-loss signal that must
// abort the switch — operator keeps running on src, resolves the
// dst, tries again.
func verifyBackendRoundTrip(ctx context.Context, src *persistData, dst Backend) error {
	back, err := dst.Load(ctx)
	if err != nil {
		return fmt.Errorf("read-back Load: %w", err)
	}
	if back == nil {
		return errors.New("read-back returned nil persistData")
	}
	type check struct {
		name     string
		srcCount int
		dstCount int
	}
	checks := []check{
		{"hosts", len(src.Hosts), len(back.Hosts)},
		{"users", len(src.Users), len(back.Users)},
		{"groups", len(src.Groups), len(back.Groups)},
		{"roles", len(src.Roles), len(back.Roles)},
		{"hypervisor_certs", len(src.HypervisorCerts), len(back.HypervisorCerts)},
		{"api_tokens", len(src.APITokens), len(back.APITokens)},
		{"snapshot_policies", len(src.SnapshotPolicies), len(back.SnapshotPolicies)},
		{"migrations", len(src.Migrations), len(back.Migrations)},
	}
	for _, c := range checks {
		if c.srcCount != c.dstCount {
			return fmt.Errorf("%s count mismatch: src=%d dst=%d", c.name, c.srcCount, c.dstCount)
		}
	}
	return nil
}
