package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"
)

func manifestFor(t *testing.T, priv ed25519.PrivateKey, digest []byte) agentBinaryManifest {
	t.Helper()
	return agentBinaryManifest{
		SHA256: hex.EncodeToString(digest),
		Sig:    base64.StdEncoding.EncodeToString(ed25519.Sign(priv, digest)),
		Alg:    "ed25519",
	}
}

func TestCheckBinarySignature_Valid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	d := sha256.Sum256([]byte("a legitimate agent binary"))
	m := manifestFor(t, priv, d[:])
	if err := checkBinarySignature(pub, nil, time.Time{}, d[:], m); err != nil {
		t.Fatalf("valid signature should verify, got %v", err)
	}
}

func TestCheckBinarySignature_Rejects(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	d := sha256.Sum256([]byte("binary"))
	m := manifestFor(t, priv, d[:])

	// Wrong (attacker) key — the signature won't match the trusted key.
	if err := checkBinarySignature(other, nil, time.Time{}, d[:], m); err == nil {
		t.Error("wrong key: expected failure")
	}
	// Digest mismatch: the downloaded bytes differ from what was signed.
	tampered := sha256.Sum256([]byte("tampered binary"))
	if err := checkBinarySignature(pub, nil, time.Time{}, tampered[:], m); err == nil {
		t.Error("digest mismatch: expected failure")
	}
	// Manifest SHA256 field doesn't match the actual digest.
	bad := m
	bad.SHA256 = hex.EncodeToString(tampered[:])
	if err := checkBinarySignature(pub, nil, time.Time{}, d[:], bad); err == nil {
		t.Error("manifest sha mismatch: expected failure")
	}
	// Garbage signature encoding.
	bad2 := m
	bad2.Sig = "!!!not-base64!!!"
	if err := checkBinarySignature(pub, nil, time.Time{}, d[:], bad2); err == nil {
		t.Error("bad signature encoding: expected failure")
	}
}

func TestCheckBinarySignature_PreviousKeyGrace(t *testing.T) {
	// Signed by the previous key; current key differs (post-rotation).
	curPub, _, _ := ed25519.GenerateKey(rand.Reader)
	prevPub, prevPriv, _ := ed25519.GenerateKey(rand.Reader)
	d := sha256.Sum256([]byte("binary signed before rotation"))
	m := manifestFor(t, prevPriv, d[:])

	// Inside grace: accepted via the previous key.
	if err := checkBinarySignature(curPub, prevPub, time.Now().Add(time.Minute), d[:], m); err != nil {
		t.Errorf("within grace should accept previous-key signature, got %v", err)
	}
	// Past grace: rejected.
	if err := checkBinarySignature(curPub, prevPub, time.Now().Add(-time.Minute), d[:], m); err == nil {
		t.Error("past grace: expected failure")
	}
}
