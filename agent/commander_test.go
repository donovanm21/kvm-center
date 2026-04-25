package main

import (
	"strings"
	"testing"
)

// TestParseMigrationProgress regression-locks the decimal-aware regex.
// virsh --verbose emits every tick with a fractional part, and an
// integer-only regex silently dropped every frame for v5.32.0 →
// v5.32.7. Keep this table covering the shapes we've seen in prod.
func TestParseMigrationProgress(t *testing.T) {
	cases := []struct {
		in      string
		wantPct int
		wantOK  bool
	}{
		{"Migration: [ 0.08 %]", 0, true},
		{"Migration: [ 1.04 %]", 1, true},
		{"Migration: [50.00 %]", 50, true},
		{"Migration: [100.00 %]", 100, true},
		{"Migration: [  7 %]", 7, true},     // no fractional, extra ws
		{"Migration: [12.34 %]", 12, true},  // mid-range
		{"random noise", 0, false},          // not a progress line
		{"Migration: complete", 0, false},   // missing brackets
		{"[ 50.00 %]", 0, false},            // missing Migration: prefix
		{"Migration: [ -1.00 %]", 0, false}, // negative rejected
		{"Migration: [.5 %]", 0, false},     // no integer part
		{"Migration: [150 %]", 0, false},    // out-of-range high (Tier 2 #7)
		{"Migration: [101.0 %]", 0, false},  // off-by-one high
	}
	for _, c := range cases {
		got, ok := parseMigrationProgress(c.in)
		if ok != c.wantOK || got != c.wantPct {
			t.Errorf("parseMigrationProgress(%q) = (%d,%v); want (%d,%v)",
				c.in, got, ok, c.wantPct, c.wantOK)
		}
	}
}

// TestMigrateProgressScanner regression-locks the \r-aware split func.
// v5.32.0 → v5.32.9 used bufio.Scanner's default (\n-only) split. virsh
// writes progress ticks with \r so they overwrite on a terminal; the
// result was that every tick from 0% to completion coalesced into one
// giant "line" the scanner wouldn't yield until the process exited.
func TestMigrateProgressScanner(t *testing.T) {
	// Real virsh-shaped input — all progress ticks carriage-return-
	// separated with a final newline at the end of the run.
	input := "Migration: [ 0.08 %]" +
		"\rMigration: [ 1.04 %]" +
		"\rMigration: [50.00 %]" +
		"\rMigration: [100.00 %]\n"

	s := newMigrateProgressScanner(strings.NewReader(input))
	var pcts []int
	for s.Scan() {
		if pct, ok := parseMigrationProgress(s.Text()); ok {
			pcts = append(pcts, pct)
		}
	}
	if err := s.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	want := []int{0, 1, 50, 100}
	if len(pcts) != len(want) {
		t.Fatalf("got %d ticks %v; want %d %v", len(pcts), pcts, len(want), want)
	}
	for i := range want {
		if pcts[i] != want[i] {
			t.Errorf("tick %d = %d; want %d", i, pcts[i], want[i])
		}
	}
}

// TestMigrateProgressScannerMixedSeparators exercises the realistic
// case where some lines end in \n (pre-migration setup messages) and
// some in \r (progress ticks).
func TestMigrateProgressScannerMixedSeparators(t *testing.T) {
	input := "starting migration\n" +
		"Migration: [ 5.00 %]" +
		"\rMigration: [50.00 %]" +
		"\nsome stderr\n"

	s := newMigrateProgressScanner(strings.NewReader(input))
	var lines []string
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	if err := s.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	want := []string{
		"starting migration",
		"Migration: [ 5.00 %]",
		"Migration: [50.00 %]",
		"some stderr",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines %v; want %d", len(lines), lines, len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q; want %q", i, lines[i], want[i])
		}
	}
}
