package main

// Demo-mode toggle for the public-facing demo deployment. When
// `KVM_CENTER_DEMO_MODE=1` is set, the server enters a hardened
// posture suitable for hosting an "anyone can log in as admin"
// instance behind the CTA on kvm-center.com:
//
//   - Admin endpoints that can persist state outside the daily wipe
//     (restore tarball, server restart, grant-signer rotation) are
//     gated to 403 even when the caller has user.manage. The demo
//     `admin` is the same user as anyone-on-the-internet, and any
//     persistence outside the Mongo wipe defeats the daily reset.
//   - Audit-log + recent-tasks IPs are redacted for non-self rows.
//     Without this, a visitor logging in as `admin` can harvest the
//     IPs of every other concurrent demo user.
//   - First-admin registration on /api/auth/register is disabled —
//     the demo seeds its own admin and a stranger can't replace it.
//
// The flag is read once at startup. `IsDemoMode()` is the public
// accessor; demo-only middleware (demoBlock403) wraps the
// endpoints listed above.

import (
	"log"
	"net/http"
	"os"
)

var demoMode bool

func loadDemoMode() {
	if os.Getenv("KVM_CENTER_DEMO_MODE") == "1" {
		demoMode = true
		log.Printf("demo-mode: KVM_CENTER_DEMO_MODE=1 — admin write paths blocked, audit IPs redacted")
	}
}

// IsDemoMode reports whether the server is running in
// hardened-demo posture.
func IsDemoMode() bool { return demoMode }

// demoBlock403 wraps an http.Handler so it returns
// 403 Forbidden + a friendly message when KVM_CENTER_DEMO_MODE=1.
// In normal (non-demo) deployments the wrapper is a no-op pass-
// through; the wrapped handler runs as if the wrapper weren't
// there.
func demoBlock403(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if demoMode {
			writeError(w, http.StatusForbidden, "this endpoint is disabled in the public demo")
			return
		}
		next.ServeHTTP(w, r)
	})
}
