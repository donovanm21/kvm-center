package main

import (
	"strings"
	"sync"
	"time"
)

// Login brute-force throttle. Tracks failed login attempts per
// (username|ip) bucket in memory; after `loginFailureLimit` strikes
// inside `loginFailureWindow`, the bucket is locked for
// `loginLockoutDuration`. A successful login clears the bucket.
//
// In-memory by design — survives within a single server process.
// Restarts wipe the state, which is acceptable: an attacker grinding
// passwords against a bounce-restarting server still pays the
// rate-limit cost between restarts, and operators rebooting the
// service shouldn't lock everyone out indefinitely.

const (
	loginFailureLimit    = 5
	loginFailureWindow   = 15 * time.Minute
	loginLockoutDuration = 15 * time.Minute
)

type loginAttempt struct {
	failures   int
	firstFail  time.Time
	lockedTill time.Time
}

var (
	loginThrottleMu sync.Mutex
	loginAttempts   = map[string]*loginAttempt{}
)

func loginThrottleKey(username, ip string) string {
	return strings.ToLower(strings.TrimSpace(username)) + "|" + ip
}

// loginThrottleCheck reports whether the (username, ip) bucket is
// currently locked out. Returns the duration remaining on the lock
// (zero when not locked).
func loginThrottleCheck(username, ip string) time.Duration {
	loginThrottleMu.Lock()
	defer loginThrottleMu.Unlock()
	a, ok := loginAttempts[loginThrottleKey(username, ip)]
	if !ok {
		return 0
	}
	if a.lockedTill.IsZero() || time.Now().After(a.lockedTill) {
		return 0
	}
	return time.Until(a.lockedTill)
}

// loginThrottleRecordFailure bumps the failure counter and locks the
// bucket once the threshold is crossed.
func loginThrottleRecordFailure(username, ip string) {
	loginThrottleMu.Lock()
	defer loginThrottleMu.Unlock()
	key := loginThrottleKey(username, ip)
	now := time.Now()
	a, ok := loginAttempts[key]
	if !ok || now.Sub(a.firstFail) > loginFailureWindow {
		a = &loginAttempt{firstFail: now}
		loginAttempts[key] = a
	}
	a.failures++
	if a.failures >= loginFailureLimit {
		a.lockedTill = now.Add(loginLockoutDuration)
	}
}

// loginThrottleClear wipes the bucket on successful login.
func loginThrottleClear(username, ip string) {
	loginThrottleMu.Lock()
	defer loginThrottleMu.Unlock()
	delete(loginAttempts, loginThrottleKey(username, ip))
}
