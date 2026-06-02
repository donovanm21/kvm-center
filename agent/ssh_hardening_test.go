package main

import "testing"

// TestSSHStrictHostKeyOpt pins the agent's two SSH host-key postures:
//   - strict OFF (default): accept-new (TOFU) — accept on first sight,
//     pin thereafter; non-interactive, but rejects a changed key (MITM).
//   - strict ON (operator opt-in): yes — requires a pre-populated
//     known_hosts and refuses any unrecognised key.
func TestSSHStrictHostKeyOpt(t *testing.T) {
	// Restore the package-global posture for any sibling tests.
	prev := sshStrictMode.Load()
	t.Cleanup(func() { setSSHStrict(prev) })

	cases := []struct {
		name   string
		strict bool
		want   string
	}{
		{name: "strict off uses accept-new TOFU", strict: false, want: "StrictHostKeyChecking=accept-new"},
		{name: "strict on requires yes", strict: true, want: "StrictHostKeyChecking=yes"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setSSHStrict(tc.strict)
			if got := sshStrictHostKeyOpt(); got != tc.want {
				t.Fatalf("sshStrictHostKeyOpt() with strict=%v = %q, want %q", tc.strict, got, tc.want)
			}
		})
	}
}
