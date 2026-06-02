package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Token replay semantics: a valid token may be reused for the length
// of its 5-minute TTL. This is necessary because a single "Launch
// Console" click produces at least two requests with the same token:
// one GET /console/<vm> for the noVNC HTML and one WebSocket upgrade
// to /ws/<vm>. noVNC reconnects also reuse the token. One-time-use
// would make the UI loop "Reconnecting…" forever on every session.
// The expiry is the guard we rely on.

// Console-token verification (v5.0.2). Before this release both
// /console/<vm> and /ws/<vm> were unauthenticated — anyone on the LAN
// could pull up a VM's VNC. We now require a short-lived HMAC token
// issued by the server's /api/vms/console-token endpoint (which gates
// on the user's vm.console permission + RBAC).
//
// Signature format: base64url(HMAC-SHA256(secret, "host|vm|exp|nonce"))
// Query string carries `token`, `exp`, `nonce`. The agent caches the
// secret at /var/lib/kvm-center-agent/console-secret (0600) and
// refreshes it from every telemetry reply so rotations propagate.

const secretCacheFile = "/var/lib/kvm-center-agent/console-secret"

// consoleSecretGraceWindow is how long the previous secret stays
// acceptable after a rotation. Longer than consoleTokenTTL
// (5 min on the server) so a token minted just before the rotation
// survives until its natural expiry. Tier S #10, v5.32.19.
const consoleSecretGraceWindow = 10 * time.Minute

var (
	consoleSecret     string
	consoleSecretPrev string
	consoleSecretRot  time.Time
	consoleSecMu      sync.RWMutex
	hostIDForToken    string
)

// SetConsoleSecret is called from the telemetry loop whenever the
// server replies with a secret. Atomic swap + persist. When the
// secret changes, the outgoing value is retained as a "previous"
// key that verifyConsoleToken will still accept for
// consoleSecretGraceWindow — this covers tokens that were minted
// with the old secret but arrive at this agent after it rotated.
func SetConsoleSecret(s string) {
	if s == "" {
		return
	}
	consoleSecMu.Lock()
	changed := s != consoleSecret
	if changed && consoleSecret != "" {
		consoleSecretPrev = consoleSecret
		consoleSecretRot = time.Now()
	}
	consoleSecret = s
	consoleSecMu.Unlock()
	if changed {
		_ = os.MkdirAll(filepath.Dir(secretCacheFile), 0o755)
		_ = os.WriteFile(secretCacheFile, []byte(s), 0o600)
		log.Printf("console-auth: cached new console-token secret (previous retained for %s)", consoleSecretGraceWindow)
	}
}

// LoadCachedConsoleSecret rehydrates the secret from disk at startup —
// so an agent restart doesn't reject console tokens until the next
// telemetry tick.
func LoadCachedConsoleSecret() {
	b, err := os.ReadFile(secretCacheFile)
	if err != nil {
		return
	}
	consoleSecMu.Lock()
	consoleSecret = string(b)
	consoleSecMu.Unlock()
}

// SetHostIDForToken records this agent's host ID so the console-token
// verifier can enforce that the token was issued FOR this host — a
// token valid on host-a shouldn't be accepted by host-b.
func SetHostIDForToken(id string) {
	consoleSecMu.Lock()
	hostIDForToken = id
	consoleSecMu.Unlock()
}

// consoleSecretsSnapshot returns the current + previous console secrets, the
// deadline through which the previous one is still accepted, and this agent's
// host id — under one read-lock. The command-auth verifier reuses the same
// rotation-tolerant key material and host binding.
func consoleSecretsSnapshot() (secret, prev string, prevUntil time.Time, host string) {
	consoleSecMu.RLock()
	defer consoleSecMu.RUnlock()
	return consoleSecret, consoleSecretPrev, consoleSecretRot.Add(consoleSecretGraceWindow), hostIDForToken
}

// verifyConsoleToken returns nil iff the token on the request query
// is current, signed for this host+vm, and the nonce hasn't been
// replayed. Fails closed for any missing field or signature mismatch.
func verifyConsoleToken(r *http.Request, vmName string) error {
	consoleSecMu.RLock()
	secret := consoleSecret
	prev := consoleSecretPrev
	prevUntil := consoleSecretRot.Add(consoleSecretGraceWindow)
	host := hostIDForToken
	consoleSecMu.RUnlock()
	if secret == "" {
		return fmt.Errorf("agent has no console-token secret yet (waiting on first telemetry reply)")
	}
	if host == "" {
		return fmt.Errorf("agent host id not set")
	}

	q := r.URL.Query()
	token := q.Get("token")
	expStr := q.Get("exp")
	nonce := q.Get("nonce")
	if token == "" || expStr == "" || nonce == "" {
		return fmt.Errorf("token, exp, and nonce query params are required")
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid exp: %v", err)
	}
	if time.Now().Unix() > exp {
		return fmt.Errorf("token expired")
	}
	if checkConsoleTokenSignature(secret, host, vmName, exp, nonce, token) {
		return nil
	}
	// Fall back to the previous secret if we're still inside the
	// post-rotation grace window. Covers tokens that were minted
	// with the old secret but arrive after the agent rotated.
	if prev != "" && time.Now().Before(prevUntil) &&
		checkConsoleTokenSignature(prev, host, vmName, exp, nonce, token) {
		return nil
	}
	return fmt.Errorf("token signature mismatch")
}

// checkConsoleTokenSignature returns true iff the token HMAC matches
// when signed with the given secret. Pulled out of verifyConsoleToken
// so verify can fall back to the previous (pre-rotation) secret.
func checkConsoleTokenSignature(secret, host, vmName string, exp int64, nonce, token string) bool {
	h := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(h, "%s|%s|%d|%s", host, vmName, exp, nonce)
	want := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}

// ParseTelemetryReply extracts the console_secret and the command_auth
// enforcement signal (if present) from the server's JSON response body.
// Unknown fields are tolerated so older servers without these fields are
// fine; command_auth defaults to false (don't enforce) when the field is
// absent, keeping a server-first rollout lockout-proof.
func ParseTelemetryReply(body []byte) {
	var reply struct {
		ConsoleSecret string `json:"console_secret"`
		CommandAuth   bool   `json:"command_auth"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return
	}
	SetConsoleSecret(reply.ConsoleSecret)
	SetCommandAuthActive(reply.CommandAuth)
}
