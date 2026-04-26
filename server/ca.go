package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Cluster CA (v5.10.0). The kvm-center server is the authority for
// hypervisor identities used by the upcoming mTLS migration channel
// (v5.10.1) and signed migration grants (v5.10.2). This file handles
// only the CA bootstrap and leaf-signing; revocation and rotation
// land in later patches.
//
// Scope decisions:
//   - ECDSA P-256 throughout — matches the existing server TLS story
//     and keeps dependencies to Go stdlib only.
//   - CA material lives on disk at <tls-dir>/ca/ca.{crt,key}. No
//     passphrase wrapping in v5.10.0; operator is expected to secure
//     the directory at the filesystem layer. A passphrase-protected
//     variant is a future enhancement.
//   - Leaf TTL 1 year; CA itself 10 years.

const (
	hypervisorLeafTTL = 365 * 24 * time.Hour
	caTTL             = 10 * 365 * 24 * time.Hour
)

// ClusterCA is the in-memory handle to the signing material. Created
// once at startup via EnsureClusterCA; methods are safe for
// concurrent use.
type ClusterCA struct {
	mu       sync.RWMutex
	certPath string
	keyPath  string
	cert     *x509.Certificate
	certPEM  []byte
	key      *ecdsa.PrivateKey
}

// EnsureClusterCA loads the CA material from disk, generating a fresh
// ECDSA P-256 root if the cert/key files are missing. Directory
// permissions default to 0700; the key file is 0600.
func EnsureClusterCA(dir string) (*ClusterCA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir ca dir: %w", err)
	}
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	_, cerr := os.Stat(certPath)
	_, kerr := os.Stat(keyPath)
	if cerr != nil || kerr != nil {
		if err := generateCA(certPath, keyPath); err != nil {
			return nil, err
		}
	}
	return loadCA(certPath, keyPath)
}

func generateCA(certPath, keyPath string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("ca keygen: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "kvm-center cluster CA",
			Organization: []string{"KVM Center"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(caTTL),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("ca self-sign: %w", err)
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}
	return writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600)
}

func loadCA(certPath, keyPath string) (*ClusterCA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read ca cert: %w", err)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, errors.New("ca cert not PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ca cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read ca key: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("ca key not PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ca key: %w", err)
	}
	return &ClusterCA{
		certPath: certPath,
		keyPath:  keyPath,
		cert:     cert,
		certPEM:  certPEM,
		key:      key,
	}, nil
}

// CertPEM returns the CA certificate in PEM form, safe to expose to
// any authenticated caller (the public half of the trust anchor).
func (c *ClusterCA) CertPEM() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]byte, len(c.certPEM))
	copy(out, c.certPEM)
	return out
}

// Cert returns the parsed CA certificate for UI display.
func (c *ClusterCA) Cert() *x509.Certificate {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cert
}

// Pool returns an *x509.CertPool containing this CA, suitable for
// configuring tls.Config.ClientCAs / RootCAs. Used by the gRPC
// listener (v5.44.0) to verify hypervisor leaf certs that agents
// present on the streaming RPC.
func (c *ClusterCA) Pool() *x509.CertPool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pool := x509.NewCertPool()
	if c.cert != nil {
		pool.AddCert(c.cert)
	}
	return pool
}

// SignHypervisorCSR validates a CSR from a hypervisor agent, issues
// a leaf certificate bound to hostID (CN), and returns the leaf in
// PEM form. The CSR's subject CN is overridden with hostID so a
// caller can't choose their own identity — the enrollment token
// determines who they are. dnsNames and ipAddrs are added to the
// SAN list so peers can connect by either hostname or IP and still
// pass libvirt's hostname-check TLS verification.
func (c *ClusterCA) SignHypervisorCSR(csrPEM []byte, hostID string, dnsNames, ipAddrs []string) (certPEM []byte, serialHex, fingerprint string, notBefore, notAfter time.Time, err error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		err = errors.New("csr not PEM")
		return
	}
	csr, perr := x509.ParseCertificateRequest(block.Bytes)
	if perr != nil {
		err = fmt.Errorf("parse csr: %w", perr)
		return
	}
	if perr := csr.CheckSignature(); perr != nil {
		err = fmt.Errorf("csr signature invalid: %w", perr)
		return
	}
	serial, serr := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if serr != nil {
		err = serr
		return
	}
	now := time.Now().UTC()
	notBefore = now.Add(-5 * time.Minute)
	notAfter = now.Add(hypervisorLeafTTL)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   hostID,
			Organization: []string{"KVM Center hypervisors"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              append([]string{hostID}, dnsNames...),
	}
	for _, raw := range ipAddrs {
		if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		}
	}
	c.mu.RLock()
	der, cerr := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	c.mu.RUnlock()
	if cerr != nil {
		err = fmt.Errorf("sign csr: %w", cerr)
		return
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	serialHex = fmt.Sprintf("%x", serial)
	fingerprint = fingerprintSHA256(der)
	return
}

func fingerprintSHA256(der []byte) string {
	sum := sha256.Sum256(der)
	return hexColonUpper(sum[:])
}

func hexColonUpper(b []byte) string {
	out := make([]byte, 0, len(b)*3-1)
	for i, x := range b {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hex.EncodeToString([]byte{x})[0], hex.EncodeToString([]byte{x})[1])
	}
	return string(out)
}
