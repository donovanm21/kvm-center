package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP / RFC 6238 implementation. SHA-1, 30-second step, 6 digits —
// the defaults every authenticator app understands (Google Authenticator,
// 1Password, Authy, Bitwarden, iOS Passwords, etc.).

const (
	totpDigits   = 6
	totpStepSecs = 30
	totpIssuer   = "KVM Center"
)

// newTOTPSecret returns a fresh 20-byte base32-encoded secret (no padding).
func newTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "="), nil
}

// totpCode computes the 6-digit code for the given secret at time t.
func totpCode(secret string, t time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).
		DecodeString(strings.ToUpper(secret))
	if err != nil {
		return "", err
	}
	counter := uint64(t.Unix() / totpStepSecs)
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0F
	val := (uint32(sum[offset])&0x7F)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, val%mod), nil
}

// verifyTOTP checks the submitted code against the current window and the
// one on either side, tolerating small clock drift between the server and
// the authenticator app (±30 s).
func verifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	now := time.Now()
	for _, delta := range []time.Duration{-totpStepSecs * time.Second, 0, totpStepSecs * time.Second} {
		expected, err := totpCode(secret, now.Add(delta))
		if err != nil {
			return false
		}
		if hmac.Equal([]byte(expected), []byte(code)) {
			return true
		}
	}
	return false
}

// otpauthURI returns the otpauth:// URI that authenticator apps scan.
// Even without a QR code, users can paste this into 1Password / Bitwarden.
func otpauthURI(username, secret string) string {
	label := url.PathEscape(totpIssuer + ":" + username)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", totpIssuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", totpDigits))
	q.Set("period", fmt.Sprintf("%d", totpStepSecs))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
