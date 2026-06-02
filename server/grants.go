package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/models"
)

// Migration grants. Control plane owns an Ed25519 key pair; before
// every mTLS migration the server mints a signed grant bound to
// {source, dest, vm, TTL}, and the destination agent validates it
// offline against the cached public key. The key lives next to the
// cluster CA on disk — rotating it invalidates outstanding grants,
// which is safe because TTL is ~5 minutes.

const (
	grantTTL = 5 * time.Minute
	// grantRotationGrace is how long a previous signer's public key
	// stays valid after Rotate(). Longer than grantTTL so every
	// grant issued under the previous key has a chance to be
	// consumed (or expire naturally) before we stop accepting it.
	// Tier S #9, v5.32.21.
	grantRotationGrace = 10 * time.Minute
)

// serverGrantSigner is the package-level handle set by main() after
// EnsureGrantSigner succeeds. Handlers reach it here rather than
// passing it through five layers of closures. nil-safe: when the
// signer isn't initialised (e.g. a no-CA test build), grant issuance
// is skipped and we fall back to the v5.10 behaviour.
var serverGrantSigner *GrantSigner

// GrantSigner owns the Ed25519 key material. During a rotation grace
// window it also carries the previous public key so in-flight grants
// issued under the old key still verify.
type GrantSigner struct {
	mu        sync.RWMutex
	priv      ed25519.PrivateKey
	pub       ed25519.PublicKey
	prevPub   ed25519.PublicKey // empty when no recent rotation
	rotatedAt time.Time         // zero when no recent rotation
	dir       string
	keyPath   string
	certPath  string // for symmetry with ClusterCA layout; unused today
}

// EnsureGrantSigner loads or generates an Ed25519 keypair under dir.
// Idempotent — first boot generates, subsequent boots load. Also
// rehydrates the previous-key state (grant.ed25519.prev.*) if the
// sidecar files exist AND the rotation timestamp is still inside
// grantRotationGrace.
func EnsureGrantSigner(dir string) (*GrantSigner, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir grant dir: %w", err)
	}
	privPath := filepath.Join(dir, "grant.ed25519.key")
	pubPath := filepath.Join(dir, "grant.ed25519.pub")

	if _, err := os.Stat(privPath); os.IsNotExist(err) {
		pub, priv, kerr := ed25519.GenerateKey(rand.Reader)
		if kerr != nil {
			return nil, fmt.Errorf("grant keygen: %w", kerr)
		}
		if werr := writePEM(privPath, "ED25519 PRIVATE KEY", priv, 0o600); werr != nil {
			return nil, werr
		}
		if werr := writePEM(pubPath, "ED25519 PUBLIC KEY", pub, 0o644); werr != nil {
			return nil, werr
		}
	}
	privBytes, err := os.ReadFile(privPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(privBytes)
	if block == nil || block.Type != "ED25519 PRIVATE KEY" {
		return nil, errors.New("grant private key: bad PEM")
	}
	priv := ed25519.PrivateKey(block.Bytes)
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("grant private key: wrong size")
	}
	g := &GrantSigner{
		priv:     priv,
		pub:      priv.Public().(ed25519.PublicKey),
		dir:      dir,
		keyPath:  privPath,
		certPath: pubPath,
	}
	// Rehydrate previous-key state if a rotation happened recently.
	// Silent no-op when any of the sidecar files are missing — the
	// normal state is "no rotation in progress".
	if prevPub, rotAt, ok := loadPrevSigner(dir); ok {
		if time.Since(rotAt) < grantRotationGrace {
			g.prevPub = prevPub
			g.rotatedAt = rotAt
		} else {
			// Stale sidecar — best-effort cleanup so we don't
			// re-examine it forever.
			_ = os.Remove(filepath.Join(dir, "grant.ed25519.prev.pub"))
			_ = os.Remove(filepath.Join(dir, "grant.ed25519.rotated-at"))
		}
	}
	return g, nil
}

// loadPrevSigner reads the previous-key sidecars if present. Returns
// (pub, rotatedAt, true) when both files decode cleanly.
func loadPrevSigner(dir string) (ed25519.PublicKey, time.Time, bool) {
	pubBytes, err := os.ReadFile(filepath.Join(dir, "grant.ed25519.prev.pub"))
	if err != nil {
		return nil, time.Time{}, false
	}
	block, _ := pem.Decode(pubBytes)
	if block == nil || block.Type != "ED25519 PUBLIC KEY" {
		return nil, time.Time{}, false
	}
	if len(block.Bytes) != ed25519.PublicKeySize {
		return nil, time.Time{}, false
	}
	stamp, err := os.ReadFile(filepath.Join(dir, "grant.ed25519.rotated-at"))
	if err != nil {
		return nil, time.Time{}, false
	}
	rotAt, err := time.Parse(time.RFC3339, strings.TrimSpace(string(stamp)))
	if err != nil {
		return nil, time.Time{}, false
	}
	return ed25519.PublicKey(block.Bytes), rotAt, true
}

// Rotate generates a new Ed25519 keypair, demotes the current key to
// "previous" (still accepted for grantRotationGrace), and persists
// both. Caller (admin endpoint) must be authorised. Returns the new
// public key for logging.
func (g *GrantSigner) Rotate() (ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("grant keygen: %w", err)
	}
	if g.dir == "" {
		return nil, errors.New("signer has no dir configured — cannot rotate")
	}
	privPath := filepath.Join(g.dir, "grant.ed25519.key")
	pubPath := filepath.Join(g.dir, "grant.ed25519.pub")
	prevPubPath := filepath.Join(g.dir, "grant.ed25519.prev.pub")
	rotatedAtPath := filepath.Join(g.dir, "grant.ed25519.rotated-at")

	g.mu.Lock()
	defer g.mu.Unlock()
	// Save current public half as the new "previous" before we
	// overwrite. We only retain the PUBLIC half of the old key —
	// sufficient to verify in-flight grants; no new grants can be
	// issued under it since we discard the private half.
	prevPubPEMOld := pem.EncodeToMemory(&pem.Block{Type: "ED25519 PUBLIC KEY", Bytes: g.pub})
	rotAt := time.Now().UTC()
	if err := os.WriteFile(prevPubPath, prevPubPEMOld, 0o644); err != nil {
		return nil, fmt.Errorf("write prev pub: %w", err)
	}
	if err := os.WriteFile(rotatedAtPath, []byte(rotAt.Format(time.RFC3339)), 0o644); err != nil {
		return nil, fmt.Errorf("write rotated-at: %w", err)
	}
	// Atomic overwrite of the primary keypair: write .tmp siblings
	// then rename so an interrupted rotate leaves the old pair
	// intact.
	newPrivPEM := pem.EncodeToMemory(&pem.Block{Type: "ED25519 PRIVATE KEY", Bytes: priv})
	newPubPEM := pem.EncodeToMemory(&pem.Block{Type: "ED25519 PUBLIC KEY", Bytes: pub})
	if err := os.WriteFile(privPath+".tmp", newPrivPEM, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(pubPath+".tmp", newPubPEM, 0o644); err != nil {
		_ = os.Remove(privPath + ".tmp")
		return nil, err
	}
	if err := os.Rename(privPath+".tmp", privPath); err != nil {
		_ = os.Remove(pubPath + ".tmp")
		return nil, err
	}
	if err := os.Rename(pubPath+".tmp", pubPath); err != nil {
		return nil, err
	}
	g.prevPub = g.pub
	g.rotatedAt = rotAt
	g.priv = priv
	g.pub = pub
	return pub, nil
}

// RotationState returns a snapshot of the current signer state for
// admin UI display. Safe to call concurrently with Issue/Rotate.
func (g *GrantSigner) RotationState() (currentHex string, previousHex string, rotatedAt time.Time, graceExpiresAt time.Time) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	currentHex = hex.EncodeToString(g.pub)
	if g.prevPub != nil {
		previousHex = hex.EncodeToString(g.prevPub)
		rotatedAt = g.rotatedAt
		graceExpiresAt = g.rotatedAt.Add(grantRotationGrace)
	}
	return
}

// PublicKey returns the signing key's public half — agents fetch
// this once and cache it to validate grants offline.
func (g *GrantSigner) PublicKey() []byte {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]byte, len(g.pub))
	copy(out, g.pub)
	return out
}

// PublicKeyHex returns the public key as a hex fingerprint for UI
// display.
func (g *GrantSigner) PublicKeyHex() string {
	return hex.EncodeToString(g.PublicKey())
}

// SignBlob signs an arbitrary message with the current signing key. Used to
// sign agent release binaries for verified self-update (C2); agents verify
// against the same key they already trust for migration grants.
func (g *GrantSigner) SignBlob(msg []byte) []byte {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return ed25519.Sign(g.priv, msg)
}

// Issue mints + signs a grant bound to a specific migration. The
// returned envelope is base64-encoded JSON safe to ship in a
// command parameter.
func (g *GrantSigner) Issue(src, dst, vmUUID, vmName, mode string) (envelope string, grant *models.MigrationGrant, err error) {
	id, err := newGrantID()
	if err != nil {
		return "", nil, err
	}
	now := time.Now().Unix()
	grant = &models.MigrationGrant{
		ID:        id,
		Source:    src,
		Dest:      dst,
		VMUUID:    vmUUID,
		VMName:    vmName,
		IssuedAt:  now,
		ExpiresAt: now + int64(grantTTL.Seconds()),
		Mode:      mode,
	}
	payload, err := json.Marshal(grant)
	if err != nil {
		return "", nil, err
	}
	g.mu.RLock()
	sig := ed25519.Sign(g.priv, payload)
	g.mu.RUnlock()
	wire, err := json.Marshal(models.SignedGrant{Payload: payload, Signature: sig})
	if err != nil {
		return "", nil, err
	}
	envelope = base64.StdEncoding.EncodeToString(wire)
	return
}

func newGrantID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "grant_" + hex.EncodeToString(b[:]), nil
}

// VerifyEnvelope decodes + signature-checks an envelope against the
// signer's public key. Used by the server for test endpoints + by
// the agent package via the same public-key bytes.
func VerifyEnvelope(pub ed25519.PublicKey, envelope string) (*models.MigrationGrant, error) {
	raw, err := base64.StdEncoding.DecodeString(envelope)
	if err != nil {
		return nil, fmt.Errorf("grant: base64 decode: %w", err)
	}
	var signed models.SignedGrant
	if err := json.Unmarshal(raw, &signed); err != nil {
		return nil, fmt.Errorf("grant: unmarshal envelope: %w", err)
	}
	if !ed25519.Verify(pub, signed.Payload, signed.Signature) {
		return nil, errors.New("grant: bad signature")
	}
	var g models.MigrationGrant
	if err := json.Unmarshal(signed.Payload, &g); err != nil {
		return nil, fmt.Errorf("grant: unmarshal payload: %w", err)
	}
	if time.Now().Unix() > g.ExpiresAt {
		return &g, errors.New("grant: expired")
	}
	return &g, nil
}
