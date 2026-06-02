package main

// SSH hardening helper (v5.44.0). The agent's SSH-transport
// migration paths used to hard-code `StrictHostKeyChecking=no`,
// which blindly accepts any host key and is MITM-able. It was
// flagged in the 2026-04-20 review (Tier 2 #8). The default is now
// `accept-new` (TOFU: trust on first use). The agent accepts and
// pins a peer's key the first time it sees it, then refuses if that
// key ever changes — non-interactive, so it stays drop-in for
// automated SSH/SCP migrations, yet detects an in-flight key swap.
// Operators on shared networks who have already populated
// known_hosts can require fully strict checking (`yes`) by setting
// the server's `Features.SSHStrictHostKeyChecking` flag.
//
// Mechanism: the server includes `strict_host_key=yes` on the
// migration command params when the flag is on. The agent reads
// it through setSSHStrict + caller picks it up via the helper.
// The dispatcher writes this once per request before the migrate
// path runs (see commander.go MIGRATE / OFFLINE_MIGRATE handlers).
//
// Operators flipping the flag need to populate
// `/var/lib/kvm-migrator/.ssh/known_hosts` on each agent first;
// otherwise SSH refuses with "host key verification failed".

import "sync/atomic"

var sshStrictMode atomic.Bool

// setSSHStrict toggles the agent's SSH posture. Called from the
// command handler at the top of MIGRATE / OFFLINE_MIGRATE based on
// the request's `strict_host_key` param.
func setSSHStrict(on bool) { sshStrictMode.Store(on) }

// sshStrictHostKeyOpt returns the value to feed to `ssh -o
// StrictHostKeyChecking=…`. "yes" when the operator has opted in
// (Features.SSHStrictHostKeyChecking + a pre-populated known_hosts),
// "accept-new" (TOFU) otherwise.
func sshStrictHostKeyOpt() string {
	if sshStrictMode.Load() {
		return "StrictHostKeyChecking=yes"
	}
	return "StrictHostKeyChecking=accept-new"
}
