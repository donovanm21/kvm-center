package main

import (
	"net/http"
	"os"
	"time"

	"kvm-center/internal/agentauth"
)

// Server→agent command-channel authentication (C1).
//
// The agent's command HTTP listener (:8081) was historically unauthenticated
// — anyone on the management network could drive destructive VM operations.
// We now sign every server→agent command request with the shared fleet
// "console secret" (already gossiped to agents via telemetry; see
// console_token.go), using the contract in internal/agentauth. The agent
// verifies the signature on its mutating routes.
//
// Rollout is lockout-proof: the server starts signing the moment it's
// upgraded and advertises command_auth in telemetry replies; agents enforce
// only once they've seen that signal (so an older server that doesn't sign
// can never lock the fleet out). commandAuthDisabled is an emergency
// escape hatch.

// commandAuthDisabled reports the operator kill-switch. When set, the server
// stops signing agent requests and stops advertising command_auth, so agents
// fall back to accepting unsigned commands. Emergency use only.
func commandAuthDisabled() bool {
	return os.Getenv("KVM_CENTER_DISABLE_AGENT_CMD_AUTH") == "1"
}

// agentAuthTransport signs every outbound request to a hypervisor agent's
// command listener so the agent can prove it came from the control plane.
// Read-only routes are signed too (harmless); the agent enforces only on
// mutating routes.
type agentAuthTransport struct {
	secret func() string
	hostID string
	base   http.RoundTripper
}

func (t *agentAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	// Clone before mutating — RoundTrippers must not modify the caller's
	// request.
	r := req.Clone(req.Context())
	if !commandAuthDisabled() {
		// Sign over host|method|path. A missing secret (Sign errors) means
		// the server hasn't minted one yet; send unsigned and let the agent
		// treat it as a not-ready-to-enforce peer, same as a pre-upgrade
		// server.
		if auth, exp, nonce, err := agentauth.Sign(t.secret(), t.hostID, req.Method, req.URL.Path, time.Now(), 0); err == nil {
			r.Header.Set(agentauth.HeaderAuth, auth)
			r.Header.Set(agentauth.HeaderExp, exp)
			r.Header.Set(agentauth.HeaderNonce, nonce)
		}
	}
	return base.RoundTrip(r)
}

// newAgentClient returns an HTTP client that signs requests to hostID's agent
// command listener. Use it for every server→agent command dial so the agent
// can authenticate the control plane.
func (s *Store) newAgentClient(hostID string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &agentAuthTransport{secret: s.GetOrCreateConsoleSecret, hostID: hostID},
	}
}
