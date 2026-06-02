// Package agentauth defines the shared HMAC contract that authenticates
// the control-plane server's command requests to a hypervisor agent's
// command HTTP listener (the historically unauthenticated :8081 channel).
//
// Both sides import this package so the canonical signing string, header
// names, and TTL can never drift between the server (signer) and the agent
// (verifier). The HMAC key is the same fleet "console secret" the server
// already gossips to every agent in its telemetry reply (see
// console_auth.go on the agent and console_token.go on the server), so no
// new secret distribution is required.
//
// A signed request carries three headers:
//
//	X-Kvmc-Cmd-Auth:  base64url(HMAC-SHA256(secret, "hostID|METHOD|PATH|exp|nonce"))
//	X-Kvmc-Cmd-Exp:   unix expiry (seconds)
//	X-Kvmc-Cmd-Nonce: random base64url nonce
//
// Binding to hostID+method+path ties a captured header to one endpoint on
// one host, and the short TTL bounds replay. Like the console-token scheme,
// replay within the TTL is tolerated: the server signs every request fresh,
// commands are idempotent via request IDs, and the threat closed here is an
// unauthenticated party on the management network, who cannot forge the MAC
// without the secret.
package agentauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Header names carried on an authenticated command request.
const (
	HeaderAuth  = "X-Kvmc-Cmd-Auth"
	HeaderExp   = "X-Kvmc-Cmd-Exp"
	HeaderNonce = "X-Kvmc-Cmd-Nonce"
)

// DefaultTTL is how long a signed request stays valid. Short because the
// server signs every request fresh; generous enough to absorb clock skew
// and transit.
const DefaultTTL = 60 * time.Second

// ErrNoSecret is returned when signing or verifying without a key.
var ErrNoSecret = errors.New("agentauth: no secret")

// mac computes the canonical HMAC for the given request fields.
func mac(secret, hostID, method, path string, exp int64, nonce string) string {
	h := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(h, "%s|%s|%s|%d|%s", hostID, strings.ToUpper(method), path, exp, nonce)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// Sign produces the header values for a command request to hostID's agent.
// path is the request URL path (no query string). now is normally
// time.Now(); ttl of 0 uses DefaultTTL.
func Sign(secret, hostID, method, path string, now time.Time, ttl time.Duration) (auth, exp, nonce string, err error) {
	if secret == "" {
		return "", "", "", ErrNoSecret
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	var b [16]byte
	if _, err = rand.Read(b[:]); err != nil {
		return "", "", "", fmt.Errorf("agentauth: nonce: %w", err)
	}
	nonce = base64.RawURLEncoding.EncodeToString(b[:])
	expUnix := now.Add(ttl).Unix()
	exp = strconv.FormatInt(expUnix, 10)
	auth = mac(secret, hostID, method, path, expUnix, nonce)
	return auth, exp, nonce, nil
}

// Verify returns nil iff the supplied header values are a valid, unexpired
// signature for this host+method+path under secret. The comparison is
// constant-time. method and path must match what the signer used (the
// agent passes its own host ID and the inbound request's method/path).
func Verify(secret, hostID, method, path, auth, exp, nonce string, now time.Time) error {
	if secret == "" {
		return ErrNoSecret
	}
	if auth == "" || exp == "" || nonce == "" {
		return errors.New("agentauth: missing auth headers")
	}
	expUnix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return fmt.Errorf("agentauth: bad exp: %w", err)
	}
	if now.Unix() > expUnix {
		return errors.New("agentauth: expired")
	}
	want := mac(secret, hostID, method, path, expUnix, nonce)
	if subtle.ConstantTimeCompare([]byte(want), []byte(auth)) != 1 {
		return errors.New("agentauth: signature mismatch")
	}
	return nil
}
