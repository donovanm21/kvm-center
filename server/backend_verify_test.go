package main

import (
	"context"
	"testing"

	"kvm-center/internal/models"
)

// stubBackend is a minimal Backend for verifyBackendRoundTrip
// testing. Load returns whatever's in `stored`.
type stubBackend struct {
	stored  *persistData
	name    string
	saveErr error
	loadErr error
}

func (b *stubBackend) Name() string { return b.name }
func (b *stubBackend) Info(_ context.Context) BackendInfo {
	return BackendInfo{Type: b.name, Healthy: true}
}
func (b *stubBackend) Close(_ context.Context) error                { return nil }
func (b *stubBackend) Load(_ context.Context) (*persistData, error) { return b.stored, b.loadErr }
func (b *stubBackend) Save(_ context.Context, d *persistData) error {
	if b.saveErr != nil {
		return b.saveErr
	}
	cp := *d
	b.stored = &cp
	return nil
}

// TestVerifyBackendRoundTrip_PassesOnMatch: a backend that reads back
// the same counts passes verification.
func TestVerifyBackendRoundTrip_PassesOnMatch(t *testing.T) {
	src := &persistData{
		Hosts: map[string]*models.Host{"h1": {ID: "h1"}, "h2": {ID: "h2"}},
		Users: map[string]*models.User{"u1": {ID: "u1"}},
	}
	dst := &stubBackend{name: "dst", stored: &persistData{
		Hosts: src.Hosts,
		Users: src.Users,
	}}
	if err := verifyBackendRoundTrip(context.Background(), src, dst); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

// TestVerifyBackendRoundTrip_FailsOnMissingRows: if the destination
// loses a host, verification must abort.
func TestVerifyBackendRoundTrip_FailsOnMissingRows(t *testing.T) {
	src := &persistData{
		Hosts: map[string]*models.Host{"h1": {ID: "h1"}, "h2": {ID: "h2"}},
	}
	dst := &stubBackend{name: "dst", stored: &persistData{
		Hosts: map[string]*models.Host{"h1": {ID: "h1"}},
	}}
	err := verifyBackendRoundTrip(context.Background(), src, dst)
	if err == nil {
		t.Fatal("verify passed despite missing host")
	}
}

// TestVerifyBackendRoundTrip_FailsOnNilReadBack: a broken Load that
// returns nil persistData fails.
func TestVerifyBackendRoundTrip_FailsOnNilReadBack(t *testing.T) {
	src := &persistData{}
	dst := &stubBackend{name: "dst", stored: nil}
	if err := verifyBackendRoundTrip(context.Background(), src, dst); err == nil {
		t.Fatal("verify passed on nil read-back")
	}
}
