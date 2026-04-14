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

var (
	consoleSecret  string
	consoleSecMu   sync.RWMutex
	hostIDForToken string
)

// SetConsoleSecret is called from the telemetry loop whenever the
// server replies with a secret. Atomic swap + persist. Safe no-op
// when the value hasn't changed (avoids burning disk I/O every tick).
func SetConsoleSecret(s string) {
	if s == "" {
		return
	}
	consoleSecMu.Lock()
	changed := s != consoleSecret
	consoleSecret = s
	consoleSecMu.Unlock()
	if changed {
		_ = os.MkdirAll(filepath.Dir(secretCacheFile), 0o755)
		_ = os.WriteFile(secretCacheFile, []byte(s), 0o600)
		log.Printf("console-auth: cached new console-token secret")
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
// token valid on hvnode01 shouldn't be accepted by hvnode02.
func SetHostIDForToken(id string) {
	consoleSecMu.Lock()
	hostIDForToken = id
	consoleSecMu.Unlock()
}

// verifyConsoleToken returns nil iff the token on the request query
// is current, signed for this host+vm, and the nonce hasn't been
// replayed. Fails closed for any missing field or signature mismatch.
func verifyConsoleToken(r *http.Request, vmName string) error {
	consoleSecMu.RLock()
	secret := consoleSecret
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
	h := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(h, "%s|%s|%d|%s", host, vmName, exp, nonce)
	want := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 {
		return fmt.Errorf("token signature mismatch")
	}
	return nil
}

// ParseTelemetryReply extracts console_secret (if present) from the
// server's JSON response body. Unknown fields are tolerated so older
// servers without the secret field are fine.
func ParseTelemetryReply(body []byte) {
	var reply struct {
		ConsoleSecret string `json:"console_secret"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return
	}
	SetConsoleSecret(reply.ConsoleSecret)
}
