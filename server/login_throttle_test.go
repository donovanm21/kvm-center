package main

import (
	"testing"
	"time"
)

// resetLoginThrottle clears the in-process bucket map between tests
// so a previous test's failures don't leak into the next.
func resetLoginThrottle(t *testing.T) {
	t.Helper()
	loginThrottleMu.Lock()
	loginAttempts = map[string]*loginAttempt{}
	loginThrottleMu.Unlock()
}

func TestLoginThrottle_LocksAfterLimit(t *testing.T) {
	resetLoginThrottle(t)
	for i := 0; i < loginFailureLimit; i++ {
		if d := loginThrottleCheck("alice", "10.0.0.1"); d > 0 {
			t.Fatalf("locked too early at attempt %d: %v", i, d)
		}
		loginThrottleRecordFailure("alice", "10.0.0.1")
	}
	if d := loginThrottleCheck("alice", "10.0.0.1"); d <= 0 {
		t.Fatalf("expected lock after %d failures, got duration=%v", loginFailureLimit, d)
	}
}

func TestLoginThrottle_SuccessClearsBucket(t *testing.T) {
	resetLoginThrottle(t)
	for i := 0; i < loginFailureLimit; i++ {
		loginThrottleRecordFailure("bob", "10.0.0.1")
	}
	if d := loginThrottleCheck("bob", "10.0.0.1"); d <= 0 {
		t.Fatalf("expected lock, got %v", d)
	}
	loginThrottleClear("bob", "10.0.0.1")
	if d := loginThrottleCheck("bob", "10.0.0.1"); d > 0 {
		t.Fatalf("clear did not unlock, still %v", d)
	}
}

func TestLoginThrottle_PerUsernameAndIP(t *testing.T) {
	resetLoginThrottle(t)
	// Lock alice at one IP; bob at the same IP and alice at a different IP must remain free.
	for i := 0; i < loginFailureLimit; i++ {
		loginThrottleRecordFailure("alice", "10.0.0.1")
	}
	if d := loginThrottleCheck("alice", "10.0.0.1"); d <= 0 {
		t.Fatal("alice@10.0.0.1 should be locked")
	}
	if d := loginThrottleCheck("alice", "10.0.0.2"); d > 0 {
		t.Fatalf("alice from a different IP should be free, got %v", d)
	}
	if d := loginThrottleCheck("bob", "10.0.0.1"); d > 0 {
		t.Fatalf("bob from the locked IP should be free, got %v", d)
	}
}

func TestLoginThrottle_KeyIsCaseInsensitive(t *testing.T) {
	resetLoginThrottle(t)
	for i := 0; i < loginFailureLimit; i++ {
		loginThrottleRecordFailure("Alice", "10.0.0.1")
	}
	if d := loginThrottleCheck("alice", "10.0.0.1"); d <= 0 {
		t.Fatalf("case-insensitive bucket: expected alice locked, got %v", d)
	}
}

func TestLoginThrottle_WindowResetsCounter(t *testing.T) {
	resetLoginThrottle(t)
	loginThrottleRecordFailure("carol", "10.0.0.1")
	loginThrottleRecordFailure("carol", "10.0.0.1")
	// Manually rewind the bucket's firstFail past the window so the
	// next failure resets the counter to 1 instead of 3. Mirrors the
	// real-time behaviour without an actual sleep.
	loginThrottleMu.Lock()
	loginAttempts[loginThrottleKey("carol", "10.0.0.1")].firstFail = time.Now().Add(-2 * loginFailureWindow)
	loginThrottleMu.Unlock()
	loginThrottleRecordFailure("carol", "10.0.0.1")
	loginThrottleMu.Lock()
	bucket := loginAttempts[loginThrottleKey("carol", "10.0.0.1")]
	loginThrottleMu.Unlock()
	if bucket == nil || bucket.failures != 1 {
		t.Fatalf("expected counter to reset to 1 after window expired, got %#v", bucket)
	}
}

func TestLoginThrottle_LockExpires(t *testing.T) {
	resetLoginThrottle(t)
	for i := 0; i < loginFailureLimit; i++ {
		loginThrottleRecordFailure("dave", "10.0.0.1")
	}
	loginThrottleMu.Lock()
	loginAttempts[loginThrottleKey("dave", "10.0.0.1")].lockedTill = time.Now().Add(-time.Second)
	loginThrottleMu.Unlock()
	if d := loginThrottleCheck("dave", "10.0.0.1"); d > 0 {
		t.Fatalf("expired lock should report 0 remaining, got %v", d)
	}
}
