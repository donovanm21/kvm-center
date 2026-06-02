package main

import (
	"strings"
	"testing"
)

// TestValidVMName locks down the libvirt-domain-name validator that
// guards every operator-supplied VM name before it reaches
// exec.Command("virsh", …) or a remote shell string. The accept set is
// deliberately the conservative intersection of "valid libvirt domain
// name" and "safe as a bare shell/argv token": ^[A-Za-z0-9_.+-]{1,64}$
// with a leading '-' additionally rejected so the name can never be
// parsed as a flag.
func TestValidVMName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		// ── Accept: normal libvirt domain names ──────────────────
		{"simple", "web01", true},
		{"rocky-with-dash", "rocky-9", true},
		{"underscore-and-dot", "vm_test.2", true},
		{"plus-edge", "ubuntu+edge", true},
		{"all-classes", "Web_01.test+edge-9", true},
		{"single-char", "a", true},
		{"digits-only", "12345", true},
		{"max-len-64", strings.Repeat("a", 64), true},

		// ── Reject: empty / length ───────────────────────────────
		{"empty", "", false},
		{"too-long-65", strings.Repeat("a", 65), false},

		// ── Reject: leading dash (flag injection) ────────────────
		{"leading-dash-rf", "-rf", false},
		{"leading-dash-flag", "--all", false},
		{"lone-dash", "-", false},

		// ── Reject: whitespace ───────────────────────────────────
		{"space-internal", "web 01", false},
		{"leading-space", " web01", false},
		{"trailing-space", "web01 ", false},
		{"tab", "web\t01", false},
		{"newline", "web01\nrm -rf /", false},

		// ── Reject: shell metacharacters ─────────────────────────
		{"semicolon", "web01;reboot", false},
		{"pipe", "web01|cat", false},
		{"command-sub", "web01$(reboot)", false},
		{"backtick", "web01`reboot`", false},
		{"and-and", "web01&&reboot", false},
		{"dollar", "web$01", false},
		{"slash", "web/01", false},
		{"glob-star", "web*", false},
		{"redirect", "web01>file", false},
		{"quote", "web'01", false},
		{"paren", "web(01)", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validVMName(tc.in)
			if tc.ok && err != nil {
				t.Errorf("validVMName(%q) = %v, want nil (should accept)", tc.in, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("validVMName(%q) = nil, want error (should reject)", tc.in)
			}
		})
	}
}

// TestVMNameRejectionMessage confirms a rejected name yields a
// non-empty, name-bearing error so the operator gets actionable
// feedback rather than a bare 500.
func TestVMNameRejectionMessage(t *testing.T) {
	err := validVMName("web01;reboot")
	if err == nil {
		t.Fatal("expected error for metacharacter name")
	}
	if !strings.Contains(err.Error(), "web01;reboot") {
		t.Errorf("error %q should echo the offending name", err.Error())
	}
}
