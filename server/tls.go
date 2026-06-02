package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TLS support for the kvm-center server. Two flags control behaviour:
//   -cert <path>  PEM-encoded X.509 certificate (or chain)
//   -key  <path>  PEM-encoded private key matching the cert
// When both paths are set the server listens with tls.ListenAndServe,
// hot-reloading the cert from disk on every handshake (mtime-cached).
// When either flag is blank, the server auto-generates an ECDSA P-256
// self-signed cert into /var/lib/kvm-center/tls/server.{crt,key} so
// HTTPS still works out of the box; the operator can later replace
// that cert via the Settings UI without restarting the process.

// CertReloader watches a cert/key pair on disk and reloads it on file
// mtime change. Install as tls.Config.GetCertificate so cert swaps take
// effect on the next TLS handshake — no restart needed when the
// operator uploads a new cert via Settings → TLS.
type CertReloader struct {
	certPath string
	keyPath  string

	mu       sync.RWMutex
	cert     *tls.Certificate
	certStat fileSnap
	keyStat  fileSnap
	loadErr  error
}

type fileSnap struct {
	modTime time.Time
	size    int64
}

func NewCertReloader(certPath, keyPath string) *CertReloader {
	return &CertReloader{certPath: certPath, keyPath: keyPath}
}

// GetCertificate is the tls.Config callback. Returns the cached cert if
// neither file has changed since last load; otherwise re-reads both
// files and atomically swaps the cache.
func (r *CertReloader) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cs, err1 := statSnap(r.certPath)
	ks, err2 := statSnap(r.keyPath)
	if err1 != nil || err2 != nil {
		// Fall back to the last good cert if a file temporarily goes
		// missing during an atomic rename — avoids serving a TLS error
		// mid-handshake while the operator's upload is in flight.
		r.mu.RLock()
		defer r.mu.RUnlock()
		if r.cert != nil {
			return r.cert, nil
		}
		if err1 != nil {
			return nil, err1
		}
		return nil, err2
	}
	r.mu.RLock()
	if r.cert != nil && cs == r.certStat && ks == r.keyStat {
		c := r.cert
		r.mu.RUnlock()
		return c, nil
	}
	r.mu.RUnlock()

	// Reload.
	r.mu.Lock()
	defer r.mu.Unlock()
	// Re-check under write lock (another request may have reloaded while
	// we were waiting).
	if r.cert != nil && cs == r.certStat && ks == r.keyStat {
		return r.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		r.loadErr = err
		if r.cert != nil {
			return r.cert, nil // stale-but-valid beats erroring
		}
		return nil, err
	}
	r.cert = &cert
	r.certStat = cs
	r.keyStat = ks
	r.loadErr = nil
	return r.cert, nil
}

// CurrentCert returns the parsed leaf X.509 cert for UI display. Forces
// a reload first so the cached cert matches whatever is on disk.
func (r *CertReloader) CurrentCert() (*x509.Certificate, error) {
	if _, err := r.GetCertificate(nil); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cert == nil || len(r.cert.Certificate) == 0 {
		return nil, errors.New("no cert loaded")
	}
	return x509.ParseCertificate(r.cert.Certificate[0])
}

func statSnap(path string) (fileSnap, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileSnap{}, err
	}
	return fileSnap{modTime: fi.ModTime(), size: fi.Size()}, nil
}

// EnsureSelfSigned guarantees both cert and key files exist at the
// given paths. If either is missing, generates an ECDSA P-256
// self-signed cert valid for 10 years with CN=<hostname> and SANs for
// localhost / 127.0.0.1. Idempotent — no-op when the pair is present.
func EnsureSelfSigned(certPath, keyPath string) error {
	_, cerr := os.Stat(certPath)
	_, kerr := os.Stat(keyPath)
	if cerr == nil && kerr == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return fmt.Errorf("mkdir tls dir: %w", err)
	}
	return GenerateSelfSigned(certPath, keyPath)
}

// GenerateSelfSigned writes a fresh self-signed cert+key pair to disk,
// overwriting any existing files. Used by first-boot bootstrap and by
// the "Regenerate self-signed" button in Settings → TLS.
func GenerateSelfSigned(certPath, keyPath string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("ecdsa keygen: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("serial: %w", err)
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "kvm-center.local"
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   host,
			Organization: []string{"KVM Center (self-signed)"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host, "localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("create cert: %w", err)
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	return nil
}

// ValidateKeyPair confirms the given PEM cert and key parse and match —
// used by the Settings upload flow before committing the new files to
// disk. Returns nil iff the pair is usable for tls.LoadX509KeyPair.
func ValidateKeyPair(certPEM, keyPEM []byte) error {
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return err
	}
	// Double-check the leaf parses so we can show subject/expiry back to
	// the caller if desired.
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return errors.New("cert is not PEM-encoded")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return fmt.Errorf("parse cert: %w", err)
	}
	return nil
}

// AtomicWritePair writes a new cert+key pair atomically via rename.
// Partial failures don't leave the server serving half a cert — either
// both files update or neither does.
func AtomicWritePair(certPath, keyPath string, certPEM, keyPEM []byte) error {
	if err := ValidateKeyPair(certPEM, keyPEM); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return err
	}
	tmpCert := certPath + ".tmp"
	tmpKey := keyPath + ".tmp"
	if err := os.WriteFile(tmpCert, certPEM, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(tmpKey, keyPEM, 0o600); err != nil {
		os.Remove(tmpCert)
		return err
	}
	if err := os.Rename(tmpCert, certPath); err != nil {
		os.Remove(tmpCert)
		os.Remove(tmpKey)
		return err
	}
	if err := os.Rename(tmpKey, keyPath); err != nil {
		return err
	}
	return nil
}

// CertFingerprintSHA256 returns the SHA-256 hex fingerprint of the DER
// form of a certificate, formatted with colons (e.g. 3A:1F:...).
func CertFingerprintSHA256(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hexColon(sum[:])
}

func hexColon(b []byte) string {
	const hex = "0123456789ABCDEF"
	out := make([]byte, 0, len(b)*3-1)
	for i, x := range b {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hex[x>>4], hex[x&0x0f])
	}
	return string(out)
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}
