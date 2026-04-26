package main

// agentClientCert (v5.44.0) loads the hypervisor leaf cert + key
// installed by `install-agent.sh` so the agent can present them on
// outbound gRPC connections. Server-side, the v5.44.0 stream
// interceptor reads the cert's CN (the host_id pinned at enrollment)
// instead of trusting the self-asserted `x-kvmc-agent-host` header.
//
// Returns (cert, true) when both files load + parse cleanly.
// Otherwise (zero, false) — caller falls back to the metadata-only
// flow that's been in place since v5.37.1.

import (
	"crypto/tls"
	"log"
	"os"
)

const (
	agentLeafCertPath = "/etc/pki/libvirt/clientcert.pem"
	agentLeafKeyPath  = "/etc/pki/libvirt/private/clientkey.pem"
)

func agentClientCert() (tls.Certificate, bool) {
	if _, err := os.Stat(agentLeafCertPath); err != nil {
		return tls.Certificate{}, false
	}
	if _, err := os.Stat(agentLeafKeyPath); err != nil {
		return tls.Certificate{}, false
	}
	cert, err := tls.LoadX509KeyPair(agentLeafCertPath, agentLeafKeyPath)
	if err != nil {
		log.Printf("grpc-client: load cert: %v — falling back to metadata-only auth", err)
		return tls.Certificate{}, false
	}
	return cert, true
}
