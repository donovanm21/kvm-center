package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Console access tokens. Problem being solved: before v5.0.2 the agent
// served /console/<vm> and /ws/<vm> with no auth — anyone on the LAN
// who could type a URL had full VM console access. Fix is an HMAC-
// signed short-lived token minted by the server (which already knows
// who the user is and can check vm.console permission) and verified
// by the agent before it ever upgrades the WebSocket to VNC.
//
// Token shape: base64url(HMAC-SHA256(secret, "host|vm|exp|nonce"))
// URL carries `token`, `exp` (unix seconds), and `nonce`. Agent has to
// see a valid signature, exp > now, and a fresh nonce (replay guard).
//
// Secret distribution: server generates once, persists in the JSON
// store, and ships to agents inside every telemetry reply. Pre-v5 the
// telemetry endpoint is already unauthenticated so this doesn't widen
// the attack surface — but it does mean anyone who can POST telemetry
// can harvest the secret. That's acceptable for a trusted-LAN homelab
// and will be tightened when we introduce agent-registration auth.

const consoleTokenTTL = 5 * time.Minute

// GetOrCreateConsoleSecret returns the HMAC secret used to sign console
// tokens, generating one on first access. Hex-encoded for JSON friendliness.
func (s *Store) GetOrCreateConsoleSecret() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consoleSecret != "" {
		return s.consoleSecret
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// Cannot realistically happen on any supported OS; crash hard
		// so the operator knows the server is starting up broken.
		panic("console-secret: rand.Read: " + err.Error())
	}
	s.consoleSecret = hex.EncodeToString(buf)
	go s.Save()
	return s.consoleSecret
}

// SignConsoleToken mints a 5-minute token authorising the given VM on
// the given host. Called from the /api/vms/console-token handler after
// RBAC has confirmed the user carries vm.console.
func (s *Store) SignConsoleToken(hostID, vmName string) (token string, expUnix int64, nonce string) {
	secret := s.GetOrCreateConsoleSecret()
	expUnix = time.Now().Add(consoleTokenTTL).Unix()
	nb := make([]byte, 12)
	_, _ = rand.Read(nb)
	nonce = base64.RawURLEncoding.EncodeToString(nb)
	mac := hmacDigest(secret, hostID, vmName, expUnix, nonce)
	return mac, expUnix, nonce
}

// hmacDigest is the shared signing routine used by both the server
// (to mint) and would be used by the agent (to verify) — agent has its
// own copy since the server and agent don't share packages, but both
// MUST produce byte-identical output for the same inputs.
func hmacDigest(secret, hostID, vmName string, exp int64, nonce string) string {
	h := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(h, "%s|%s|%d|%s", hostID, vmName, exp, nonce)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// handleConsoleToken — POST /api/vms/console-token?host=<id>&vm=<name>
// Returns {url, token, exp, nonce}. The SPA opens url in a popup; the
// agent verifies the signature before serving anything.
func handleConsoleToken(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		hostID := r.URL.Query().Get("host")
		vmName := r.URL.Query().Get("vm")
		if hostID == "" || vmName == "" {
			writeError(w, http.StatusBadRequest, "host and vm query params required")
			return
		}
		// Global feature gate: keep parity with how /api/command honours
		// disable_console; refuse to mint tokens when consoles are off.
		if featureBlocked(w, store, "console", "remote console") {
			return
		}
		h := store.GetHost(hostID)
		if h == nil || h.IP == "" {
			writeError(w, http.StatusNotFound, "host not found or no IP")
			return
		}
		port := h.AgentPort
		if port == 0 {
			port = 8081
		}
		token, exp, nonce := store.SignConsoleToken(hostID, vmName)

		consoleURL := fmt.Sprintf("http://%s:%d/console/%s?token=%s&exp=%d&nonce=%s",
			h.IP, port, url.PathEscape(vmName),
			url.QueryEscape(token), exp, url.QueryEscape(nonce))
		log.Printf("console-token: minted for vm=%s host=%s exp=%d", vmName, hostID, exp)
		writeJSON(w, http.StatusOK, map[string]any{
			"url":   consoleURL,
			"token": token,
			"exp":   exp,
			"nonce": nonce,
		})
	}
}

// Harmless helper for unit-testing the token string format — used when
// we add agent-side verification tests.
func encodeConsoleTokenParams(token string, exp int64, nonce string) string {
	v := url.Values{}
	v.Set("token", token)
	v.Set("exp", fmt.Sprintf("%d", exp))
	v.Set("nonce", nonce)
	return v.Encode()
}

// Wire the shared secret into every telemetry reply so agents pick it
// up and cache it. Consumed by handleTelemetry — see handlers.go.
func telemetrySecretField(store *Store) json.RawMessage {
	b, _ := json.Marshal(store.GetOrCreateConsoleSecret())
	return b
}

// Ensure strings import is actually referenced (keeps linters happy if
// the file ever loses its strings usage during refactors).
var _ = strings.ToLower
