package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"
)

// Cluster CA unit tests (v5.12.0). The CA signs hypervisor leaf
// certs that libvirt's qemu+tls:// handshake trusts, so a regression
// here silently breaks the mTLS channel. These tests cover:
//
//   - Fresh-generation path produces a CA cert that validates against
//     itself and sets IsCA + CertSign key usage.
//   - Second EnsureClusterCA call on the same dir is a no-op (reloads
//     the same key rather than regenerating).
//   - SignHypervisorCSR overrides the CSR subject CN with the host ID
//     (prevents an agent from claiming a host it wasn't enrolled for)
//     and includes IP + DNS SANs.
//   - Signed leaf verifies against the CA pool.

func TestEnsureClusterCA_FreshGeneration(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureClusterCA(dir)
	if err != nil {
		t.Fatalf("EnsureClusterCA: %v", err)
	}
	cert := ca.Cert()
	if !cert.IsCA {
		t.Errorf("CA cert IsCA=false")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Errorf("CA cert lacks CertSign KeyUsage")
	}
	if cert.NotAfter.Before(time.Now().AddDate(9, 0, 0)) {
		t.Errorf("CA NotAfter should be ~10 years out, got %v", cert.NotAfter)
	}
	// Self-signature verifies.
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Errorf("CA self-signature invalid: %v", err)
	}
}

func TestEnsureClusterCA_Idempotent(t *testing.T) {
	dir := t.TempDir()
	first, err := EnsureClusterCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureClusterCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cert().SerialNumber.Cmp(second.Cert().SerialNumber) != 0 {
		t.Errorf("second EnsureClusterCA produced a new serial — should have reloaded")
	}
}

func TestSignHypervisorCSR_OverridesCNAndAddsSANs(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureClusterCA(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Build a CSR that claims a DIFFERENT CN than the one we'll pass
	// to SignHypervisorCSR — the server must win.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrTmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: "attacker"}}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTmpl, priv)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	leafPEM, _, _, _, _, err := ca.SignHypervisorCSR(csrPEM,
		"hvnode01.example",
		[]string{"hvnode01"}, // extra DNS
		[]string{"10.0.0.5"}, // IP SAN
	)
	if err != nil {
		t.Fatalf("SignHypervisorCSR: %v", err)
	}

	block, _ := pem.Decode(leafPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName != "hvnode01.example" {
		t.Errorf("CN override failed: got %q want %q", leaf.Subject.CommonName, "hvnode01.example")
	}
	// Subject override means the "attacker" CN is NOT present.
	if leaf.Subject.CommonName == "attacker" {
		t.Errorf("caller-supplied CN leaked into signed cert")
	}
	// Host ID must appear as a DNS SAN in addition to the CN.
	foundHostID := false
	for _, d := range leaf.DNSNames {
		if d == "hvnode01.example" || d == "hvnode01" {
			foundHostID = true
			break
		}
	}
	if !foundHostID {
		t.Errorf("host ID missing from DNS SANs: %v", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) != 1 || leaf.IPAddresses[0].String() != "10.0.0.5" {
		t.Errorf("IP SAN not honoured: %v", leaf.IPAddresses)
	}

	// Leaf verifies against the CA.
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert())
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("leaf doesn't chain to CA: %v", err)
	}
}

func TestSignHypervisorCSR_RejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureClusterCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, err := ca.SignHypervisorCSR([]byte("not PEM"), "hv", nil, nil); err == nil {
		t.Error("expected error on non-PEM CSR")
	}
	// Valid PEM, garbage contents.
	bad := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte{0x00, 0x01}})
	if _, _, _, _, _, err := ca.SignHypervisorCSR(bad, "hv", nil, nil); err == nil {
		t.Error("expected error on garbage CSR")
	}
}
