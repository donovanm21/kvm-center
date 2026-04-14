package main

import (
	"strings"
	"testing"
)

// kvmctl helper tests (v5.12.0). The mask function is what `kvmctl
// config` shows by default — a regression that prints the full
// secret is a credential leak.

func TestMaskToken(t *testing.T) {
	full := "kvmc_abcd1234.deadbeefcafebabe1234567890abcdef0123456789abcdef0123456789"
	m := maskToken(full)
	if strings.Contains(m, "deadbeef") {
		t.Errorf("mask leaked secret body: %s", m)
	}
	if !strings.HasPrefix(m, "kvmc_abcd1234.") {
		t.Errorf("mask should keep public prefix: %s", m)
	}
	if !strings.HasSuffix(m, full[len(full)-4:]) {
		t.Errorf("mask should keep last 4 of secret: %s", m)
	}
}

func TestMaskToken_Empty(t *testing.T) {
	if maskToken("") != "(none)" {
		t.Error("empty token should mask to (none)")
	}
}

func TestMaskToken_Short(t *testing.T) {
	// Short tokens (malformed) shouldn't panic.
	if maskToken("kvmc_x") == "" {
		t.Error("short token masked to empty")
	}
}
