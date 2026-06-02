package main

import (
	"strings"
	"testing"
)

// XML mutation tests for the v5.11.7 advanced-modal options. These
// two functions mutate the domain XML on its way to the destination;
// regressions silently miscorrupt a migrated VM's identity or
// network. Cover what we've actually seen in production:
//
//   - libvirt emits single-quoted attributes from dumpxml.
//   - Mixed quote styles (hand-edited XML) still rewrite.
//   - Zero-match input returns a legible count so the caller can log
//     "regenerated N" instead of a silent success.

func TestRegenerateAllMACs_SingleQuoted(t *testing.T) {
	in := `<interface type='bridge'>
  <mac address='52:54:00:aa:bb:cc'/>
  <source bridge='br0'/>
</interface>`
	out, n := regenerateAllMACsCount(in)
	if n != 1 {
		t.Errorf("expected 1 replacement, got %d", n)
	}
	if strings.Contains(out, "52:54:00:aa:bb:cc") {
		t.Errorf("original MAC still present: %s", out)
	}
	if !strings.Contains(out, "52:54:00:") {
		t.Errorf("replacement MAC missing KVM OUI: %s", out)
	}
}

func TestRegenerateAllMACs_DoubleQuoted(t *testing.T) {
	in := `<mac address="52:54:00:11:22:33"/>`
	out, n := regenerateAllMACsCount(in)
	if n != 1 || strings.Contains(out, "52:54:00:11:22:33") {
		t.Errorf("double-quote path failed: n=%d out=%s", n, out)
	}
}

func TestRegenerateAllMACs_Multiple(t *testing.T) {
	in := `<mac address='aa:bb:cc:dd:ee:01'/>
<mac address="aa:bb:cc:dd:ee:02"/>
<mac address='aa:bb:cc:dd:ee:03'/>`
	_, n := regenerateAllMACsCount(in)
	if n != 3 {
		t.Errorf("expected 3 replacements across mixed quote styles, got %d", n)
	}
}

func TestRegenerateAllMACs_NoMatch(t *testing.T) {
	in := `<domain><name>foo</name></domain>`
	out, n := regenerateAllMACsCount(in)
	if n != 0 {
		t.Errorf("expected 0 replacements, got %d", n)
	}
	if out != in {
		t.Errorf("no-match path should be a no-op, got %q", out)
	}
}

func TestReplaceDomainName(t *testing.T) {
	in := `<domain type='kvm'>
  <name>orig</name>
  <uuid>abc</uuid>
</domain>`
	out := replaceDomainName(in, "renamed")
	if !strings.Contains(out, "<name>renamed</name>") {
		t.Errorf("rename did not apply: %s", out)
	}
	if strings.Contains(out, "<name>orig</name>") {
		t.Errorf("original name still present: %s", out)
	}
	if !strings.Contains(out, "<uuid>abc</uuid>") {
		t.Errorf("unrelated content damaged: %s", out)
	}
}

func TestReplaceDomainName_NoMatchIsNoop(t *testing.T) {
	in := `<domain></domain>`
	out := replaceDomainName(in, "whatever")
	if out != in {
		t.Errorf("no <name> element should be a no-op, got %q", out)
	}
}

func TestRandomLocalMAC_Format(t *testing.T) {
	for i := 0; i < 16; i++ {
		m := randomLocalMAC()
		if !strings.HasPrefix(m, "52:54:00:") {
			t.Errorf("MAC missing KVM OUI prefix: %s", m)
		}
		if len(m) != 17 {
			t.Errorf("MAC wrong length: %s", m)
		}
	}
}
