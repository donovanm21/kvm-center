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

const grantTTL = 5 * time.Minute

// serverGrantSigner is the package-level handle set by main() after
// EnsureGrantSigner succeeds. Handlers reach it here rather than
// passing it through five layers of closures. nil-safe: when the
// signer isn't initialised (e.g. a no-CA test build), grant issuance
// is skipped and we fall back to the v5.10 behaviour.
var serverGrantSigner *GrantSigner

// GrantSigner owns the Ed25519 key material.
type GrantSigner struct {
	mu       sync.RWMutex
	priv     ed25519.PrivateKey
	pub      ed25519.PublicKey
	keyPath  string
	certPath string // for symmetry with ClusterCA layout; unused today
}

// EnsureGrantSigner loads or generates an Ed25519 keypair under dir.
// Idempotent — first boot generates, subsequent boots load.
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
	return &GrantSigner{
		priv:     priv,
		pub:      priv.Public().(ed25519.PublicKey),
		keyPath:  privPath,
		certPath: pubPath,
	}, nil
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
