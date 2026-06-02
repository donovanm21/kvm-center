package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// secret_box.go — envelope encryption for secrets that need to live
// at rest (LDAP bind passwords, OIDC client_secrets, future SAML
// signing keys, etc.). Pre-v5.50 these landed plaintext in MongoDB
// and JSON backups; anyone with read on the support-bundle / mongo
// dump recovered them and could pivot into the operator's directory
// or IdP.
//
// Key management:
//   - KVM_CENTER_DATA_KEY (base64 of 32 raw bytes) overrides everything;
//     suitable for KMS-derived material or operator-supplied secrets.
//   - Otherwise we read $KVM_CENTER_CA_DIR/data.key (auto-generated at
//     first boot, mode 0600). Living next to the cluster CA private key
//     is intentional: any backup that takes the CA already takes
//     enough trust material that protecting the data-key in a
//     separate volume buys nothing operationally meaningful, while
//     having a single secret-volume keeps DR predictable.
//   - Tests + in-memory backends without a CA dir use a per-process
//     ephemeral key; envelope-encrypted values won't survive a
//     restart, which is fine for unit tests.
//
// On-disk format:
//   - Plaintext input: any UTF-8 string (passwords, secrets).
//   - Ciphertext output: base64(nonce ‖ aead_seal(plaintext)) — single
//     line, embeddable in JSON. Prefix "kvmc-enc:v1:" identifies
//     v5.50-and-later format so future key rotations can detect
//     legacy plaintext.

const (
	secretEnvelopePrefix = "kvmc-enc:v1:"
	secretKeyFileName    = "data.key"
	secretKeySize        = 32 // AES-256
)

var (
	secretBoxKey []byte // populated at startup; nil until loadSecretBox()

	errSecretBoxNotInitialised = errors.New("secret_box: not initialised — call loadSecretBox() at startup")
)

// loadSecretBox initialises the package-level encryption key.
// Called once from main() after the CA dir is known. Returns an
// error only on misconfiguration (malformed env, unwritable disk);
// otherwise sets secretBoxKey and returns nil.
func loadSecretBox(caDir string) error {
	if raw := os.Getenv("KVM_CENTER_DATA_KEY"); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("secret_box: KVM_CENTER_DATA_KEY base64 decode: %w", err)
		}
		if len(key) != secretKeySize {
			return fmt.Errorf("secret_box: KVM_CENTER_DATA_KEY must be %d bytes (got %d) — generate via `openssl rand -base64 32`", secretKeySize, len(key))
		}
		secretBoxKey = key
		log.Printf("secret_box: using key from KVM_CENTER_DATA_KEY env")
		return nil
	}
	// Disk-backed key, created on first boot.
	if caDir == "" {
		// Test / in-memory mode — generate ephemeral.
		secretBoxKey = make([]byte, secretKeySize)
		if _, err := rand.Read(secretBoxKey); err != nil {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(caDir, 0o700); err != nil {
		return fmt.Errorf("secret_box: cannot ensure CA dir: %w", err)
	}
	path := filepath.Join(caDir, secretKeyFileName)
	if b, err := os.ReadFile(path); err == nil {
		// Stored as base64 so a curious operator can copy it into the
		// env if they want to migrate to KMS later.
		key, decErr := base64.StdEncoding.DecodeString(string(b))
		if decErr == nil && len(key) == secretKeySize {
			secretBoxKey = key
			log.Printf("secret_box: loaded key from %s", path)
			return nil
		}
		log.Printf("secret_box: %s exists but is malformed (%v / len=%d) — regenerating", path, decErr, len(key))
	}
	// Fresh key.
	key := make([]byte, secretKeySize)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("secret_box: rand: %w", err)
	}
	enc := []byte(base64.StdEncoding.EncodeToString(key))
	if err := os.WriteFile(path, enc, 0o600); err != nil {
		return fmt.Errorf("secret_box: write %s: %w", path, err)
	}
	secretBoxKey = key
	log.Printf("secret_box: generated new key at %s", path)
	return nil
}

// EncryptSecret wraps a plaintext secret in the envelope format.
// Returns the ciphertext as a single-line string suitable for JSON
// embedding. Returns the plaintext unchanged if it's already
// envelope-prefixed (idempotent — safe for re-saves).
func EncryptSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if isEncryptedSecret(plaintext) {
		return plaintext, nil
	}
	if secretBoxKey == nil {
		return "", errSecretBoxNotInitialised
	}
	block, err := aes.NewCipher(secretBoxKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return secretEnvelopePrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// DecryptSecret reverses EncryptSecret. If the input is NOT envelope-
// prefixed it's returned unchanged — covers two cases:
//  1. Tests / dev that don't bother encrypting
//  2. v5.49-and-earlier records still in the database that haven't
//     been re-encrypted yet (handler should re-save on first read so
//     the next persist tick lands the encrypted form).
func DecryptSecret(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	if !isEncryptedSecret(ciphertext) {
		return ciphertext, nil
	}
	if secretBoxKey == nil {
		return "", errSecretBoxNotInitialised
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext[len(secretEnvelopePrefix):])
	if err != nil {
		return "", fmt.Errorf("secret_box: base64: %w", err)
	}
	block, err := aes.NewCipher(secretBoxKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("secret_box: ciphertext too short")
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("secret_box: open: %w", err)
	}
	return string(pt), nil
}

func isEncryptedSecret(s string) bool {
	return len(s) > len(secretEnvelopePrefix) && s[:len(secretEnvelopePrefix)] == secretEnvelopePrefix
}
