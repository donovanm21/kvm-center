package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"kvm-center/internal/agentauth"
)

// Command-channel authentication (C1). The server signs every command
// request with the shared fleet console secret (see internal/agentauth and
// console_auth.go); this middleware verifies that signature on the agent's
// mutating, server-initiated routes.
//
// Enforcement is gated on commandAuthActive, set from the server's telemetry
// reply (`command_auth: true`). This makes the rollout lockout-proof: a
// freshly-upgraded agent enforces as soon as it learns the server is
// signing, and the flag is persisted so a restarted agent enforces from boot
// (no open window). An older server that doesn't sign never sets the flag,
// so it can never lock itself out.

const commandAuthStateFile = "/var/lib/kvm-center-agent/command-auth-active"

// commandAuthActive is true once the server has advertised that it signs
// command requests. Until then the agent accepts unsigned commands so a
// server-first rollout never breaks.
var commandAuthActive atomic.Bool

// SetCommandAuthActive records whether the server signs command requests,
// persisting the flag so a restart enforces immediately. Called from the
// telemetry-reply parser.
func SetCommandAuthActive(active bool) {
	if commandAuthActive.Swap(active) == active {
		return // unchanged
	}
	_ = os.MkdirAll(filepath.Dir(commandAuthStateFile), 0o755)
	if active {
		_ = os.WriteFile(commandAuthStateFile, []byte("1"), 0o600)
		log.Printf("command-auth: server signs command requests — enforcing signatures on mutating routes")
	} else {
		_ = os.Remove(commandAuthStateFile)
		log.Printf("command-auth: server no longer signs command requests — accepting unsigned (kill-switch?)")
	}
}

// LoadCachedCommandAuthActive rehydrates the enforcement flag at startup so a
// restarted agent enforces from boot rather than waiting for the first
// telemetry tick.
func LoadCachedCommandAuthActive() {
	if _, err := os.Stat(commandAuthStateFile); err == nil {
		commandAuthActive.Store(true)
	}
}

// requireCommandAuth wraps a mutating handler with command-channel auth.
// When enforcement isn't active it passes through (back-compat). When active
// it verifies the request signature against the current console secret (with
// the post-rotation previous-secret grace), failing closed otherwise.
func requireCommandAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !commandAuthActive.Load() {
			next(w, r)
			return
		}
		secret, prev, prevUntil, host := consoleSecretsSnapshot()
		if secret == "" {
			// Active but we have no secret yet — fail closed. In practice a
			// host only receives commands after it has appeared via
			// telemetry, which is the same reply that carries the secret.
			http.Error(w, "command auth required: agent has no secret yet", http.StatusServiceUnavailable)
			return
		}
		auth := r.Header.Get(agentauth.HeaderAuth)
		exp := r.Header.Get(agentauth.HeaderExp)
		nonce := r.Header.Get(agentauth.HeaderNonce)
		now := time.Now()
		if agentauth.Verify(secret, host, r.Method, r.URL.Path, auth, exp, nonce, now) == nil {
			next(w, r)
			return
		}
		// Tolerate the previous secret inside the post-rotation grace window,
		// mirroring console-token verification.
		if prev != "" && now.Before(prevUntil) &&
			agentauth.Verify(prev, host, r.Method, r.URL.Path, auth, exp, nonce, now) == nil {
			next(w, r)
			return
		}
		log.Printf("command-auth: rejected %s %s from %s (missing/invalid signature)", r.Method, r.URL.Path, r.RemoteAddr)
		http.Error(w, "command authentication failed", http.StatusUnauthorized)
	}
}
