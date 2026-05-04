package main

import (
	"crypto/tls"
	"crypto/x509"
	"log"
	"os"
	"sync"
)

// tls_trust.go — single source of truth for the agent's outbound TLS
// trust decisions. Default = InsecureSkipVerify (homelab pairing with
// the server's auto-generated self-signed cert). Production posture =
// KVM_CENTER_AGENT_VERIFY_TLS=1, optionally pointing at a CA bundle
// via KVM_CENTER_SERVER_CA so internal/private CAs are pinned without
// needing the host trust store.
//
// Used by every outbound surface: telemetry POST (httpClient in
// main.go), gRPC migration-progress stream (grpc_client.go), live-
// metrics stream (live_metrics.go), known-hosts sync (known_hosts_
// sync.go). All of them call agentServerTLSConfig() rather than
// hand-rolling tls.Config.

var (
	tlsTrustOnce sync.Once
	tlsTrustPool *x509.CertPool
	tlsTrustVerify bool
)

func loadTLSTrust() {
	tlsTrustOnce.Do(func() {
		tlsTrustVerify = os.Getenv("KVM_CENTER_AGENT_VERIFY_TLS") == "1"
		if !tlsTrustVerify {
			return
		}
		caPath := os.Getenv("KVM_CENTER_SERVER_CA")
		if caPath == "" {
			return
		}
		pem, err := os.ReadFile(caPath)
		if err != nil {
			log.Fatalf("kvm-center-agent: KVM_CENTER_SERVER_CA=%q unreadable: %v", caPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			log.Fatalf("kvm-center-agent: KVM_CENTER_SERVER_CA=%q contains no PEM certs", caPath)
		}
		tlsTrustPool = pool
	})
}

// agentServerTLSConfig returns a *tls.Config matching the agent's
// configured trust posture. Callers can mutate Certificates / NextProtos
// freely — each call produces a fresh value.
func agentServerTLSConfig() *tls.Config {
	loadTLSTrust()
	cfg := &tls.Config{InsecureSkipVerify: !tlsTrustVerify} //nolint:gosec // see file header
	if tlsTrustVerify && tlsTrustPool != nil {
		cfg.RootCAs = tlsTrustPool
	}
	return cfg
}
