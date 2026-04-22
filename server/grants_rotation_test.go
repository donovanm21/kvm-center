package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"

	"kvm-center/internal/models"
)

// TestGrantSigner_Rotate_KeepsPrevious: rotation demotes the current
// public key to previous + stamps rotatedAt.
func TestGrantSigner_Rotate_KeepsPrevious(t *testing.T) {
	dir := t.TempDir()
	g, err := EnsureGrantSigner(dir)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	oldPub := append(ed25519.PublicKey(nil), g.pub...)

	if _, err := g.Rotate(); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	cur, prev, rotAt, grace := g.RotationState()
	if cur == "" {
		t.Fatal("current pubkey empty after rotate")
	}
	if prev == "" {
		t.Fatal("previous pubkey empty after rotate")
	}
	if !rotAt.After(time.Time{}) {
		t.Fatal("rotatedAt not stamped")
	}
	if grace.Sub(rotAt) < grantRotationGrace-time.Second {
		t.Fatalf("grace expires too soon: %s → %s", rotAt, grace)
	}
	// Previous should match the old public key.
	if prev != hexOf(oldPub) {
		t.Fatalf("previous pubkey mismatch: want %s got %s", hexOf(oldPub)[:16], prev[:16])
	}
	// Current should NOT match the old one.
	if cur == hexOf(oldPub) {
		t.Fatal("current pubkey unchanged after rotate")
	}
}

// TestGrantSigner_GrantFromOldKeyVerifies: a grant signed with the
// pre-rotation key is still verifiable after rotation (used by the
// agent's fallback path).
func TestGrantSigner_GrantFromOldKeyVerifies(t *testing.T) {
	dir := t.TempDir()
	g, err := EnsureGrantSigner(dir)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	envelope, _, err := g.Issue("hvA", "hvB", "vm-uuid-1", "vm1", "live")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// Rotate. Envelope was signed with the OLD key.
	if _, err := g.Rotate(); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// Decode the envelope manually and check both signers.
	raw, err := base64.StdEncoding.DecodeString(envelope)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	var signed models.SignedGrant
	if err := json.Unmarshal(raw, &signed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Current signer must NOT verify the old-key signature.
	if ed25519.Verify(g.pub, signed.Payload, signed.Signature) {
		t.Fatal("current pub key unexpectedly verified old-key grant")
	}
	// Previous signer MUST verify.
	if !ed25519.Verify(g.prevPub, signed.Payload, signed.Signature) {
		t.Fatal("previous pub key failed to verify pre-rotation grant")
	}
}

// TestGrantSigner_StalePrevExpiresOnReload: after > grace, a fresh
// EnsureGrantSigner discards the previous-key sidecar.
func TestGrantSigner_StalePrevExpiresOnReload(t *testing.T) {
	dir := t.TempDir()
	g, err := EnsureGrantSigner(dir)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := g.Rotate(); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	// Backdate the rotation stamp past the grace window.
	if err := rewriteRotatedAt(dir, time.Now().Add(-2*grantRotationGrace)); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// Fresh load — should drop the prev state.
	g2, err := EnsureGrantSigner(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	_, prev, _, _ := g2.RotationState()
	if prev != "" {
		t.Fatal("stale previous key was retained past grace window")
	}
}

func hexOf(pub ed25519.PublicKey) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(pub)*2)
	for _, b := range pub {
		out = append(out, hexdigits[b>>4], hexdigits[b&0xf])
	}
	return string(out)
}

func rewriteRotatedAt(dir string, t time.Time) error {
	return os.WriteFile(dir+"/grant.ed25519.rotated-at", []byte(t.UTC().Format(time.RFC3339)), 0o644)
}
