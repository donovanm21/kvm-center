package main

import (
	"strings"
	"testing"
)

// Pure-function coverage for the EDIT_VM XML patch path. The outer
// handler in commander.go shells out to virsh and can't be unit
// tested without a real libvirt install, but the domain-XML
// surgery is pure string work and locking its contract here
// catches the whole class of "libvirt refused define after our
// edit" regressions. P0.5 (v5.36).

const sampleDomainXML = `<domain type='kvm'>
  <name>demo</name>
  <memory unit='KiB'>4194304</memory>
  <vcpu placement='static'>2</vcpu>
  <os>
    <type arch='x86_64' machine='pc-q35-7.2'>hvm</type>
    <boot dev='hd'/>
    <boot dev='cdrom'/>
  </os>
  <cpu mode='host-model' check='partial' migratable='on'>
    <topology sockets='1' dies='1' cores='2' threads='1'/>
  </cpu>
  <devices>
    <video>
      <model type='virtio' heads='1' primary='yes'/>
    </video>
  </devices>
</domain>`

func TestPatchDomainXML_CPUMode(t *testing.T) {
	cases := []struct {
		mode        string
		wantContain []string
		wantReject  []string // tokens that must NOT be present in <cpu> opener
	}{
		{
			mode:        "host-passthrough",
			wantContain: []string{"mode='host-passthrough'", "check='none'", "migratable='on'"},
		},
		{
			mode:        "custom",
			wantContain: []string{"mode='custom'", "match='exact'", "check='partial'"},
			wantReject:  []string{"migratable='on'"},
		},
		{
			mode:        "host-model",
			wantContain: []string{"mode='host-model'", "check='partial'"},
			wantReject:  []string{"migratable='on'"},
		},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			out, err := patchDomainXML(sampleDomainXML, map[string]string{"cpu_mode": c.mode})
			if err != nil {
				t.Fatalf("patch failed: %v", err)
			}
			// Snip out the <cpu ...> opener so we're asserting only on
			// that element.
			cpuStart := strings.Index(out, "<cpu")
			if cpuStart < 0 {
				t.Fatalf("no <cpu> in output: %s", out)
			}
			cpuEnd := strings.IndexByte(out[cpuStart:], '>')
			opener := out[cpuStart : cpuStart+cpuEnd+1]
			for _, want := range c.wantContain {
				if !strings.Contains(opener, want) {
					t.Errorf("opener %q missing %q", opener, want)
				}
			}
			for _, rej := range c.wantReject {
				if strings.Contains(opener, rej) {
					t.Errorf("opener %q unexpectedly contains %q", opener, rej)
				}
			}
		})
	}
}

func TestPatchDomainXML_BootDev(t *testing.T) {
	out, err := patchDomainXML(sampleDomainXML, map[string]string{"boot_dev": "cdrom"})
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	// Only the FIRST <boot> is touched — the second is preserved so
	// boot-order fallbacks stay intact.
	firstIdx := strings.Index(out, "<boot ")
	if firstIdx < 0 {
		t.Fatal("no <boot> in output")
	}
	firstEnd := strings.Index(out[firstIdx:], "/>")
	firstTag := out[firstIdx : firstIdx+firstEnd+2]
	if !strings.Contains(firstTag, "dev='cdrom'") {
		t.Errorf("first <boot> not rewritten: %q", firstTag)
	}
	// Second <boot> must still exist to preserve fallback order.
	if strings.Count(out, "<boot ") != 2 {
		t.Errorf("expected two <boot> elements, saw %d", strings.Count(out, "<boot "))
	}
}

func TestPatchDomainXML_VideoModel(t *testing.T) {
	out, err := patchDomainXML(sampleDomainXML, map[string]string{"video_model": "qxl"})
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	if !strings.Contains(out, "<model type='qxl'") {
		t.Errorf("video model not rewritten: %s", out)
	}
	// <cpu>'s topology should be untouched.
	if !strings.Contains(out, "<topology sockets='1'") {
		t.Errorf("cpu topology was clobbered: %s", out)
	}
}

func TestPatchDomainXML_UnsupportedKey(t *testing.T) {
	_, err := patchDomainXML(sampleDomainXML, map[string]string{"bogus": "x"})
	if err == nil {
		t.Fatal("expected error for unsupported key")
	}
	if !strings.Contains(err.Error(), "unsupported XML edit key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReplaceTagAttr_CPUVsCPUTune(t *testing.T) {
	// <cputune> must NOT match <cpu …> replaceTagAttr. This was the
	// regression that motivated the leading-char guard at
	// collector/commander interface — without it, replaceTagAttr
	// would rewrite <cputune mode='…'> instead of <cpu mode='…'>.
	in := `<cputune>
  <vcpupin vcpu='0' cpuset='2'/>
</cputune>
<cpu mode='host-model'/>`
	out, err := replaceTagAttrFirst(in, "cpu", "mode", "custom")
	if err != nil {
		t.Fatalf("replace failed: %v", err)
	}
	if !strings.Contains(out, "<cpu mode='custom'") {
		t.Errorf("cpu not rewritten: %s", out)
	}
	if !strings.Contains(out, "<cputune>") {
		t.Errorf("cputune was accidentally rewritten: %s", out)
	}
}

func TestRewriteCPUMode_SelfClosingPreserved(t *testing.T) {
	in := `<domain><cpu mode='host-model'/></domain>`
	out, err := rewriteCPUMode(in, "host-passthrough")
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	if !strings.Contains(out, "<cpu mode='host-passthrough' check='none' migratable='on'/>") {
		t.Errorf("self-closing not preserved: %s", out)
	}
}

func TestContainsEditRecognisesBothQuoteStyles(t *testing.T) {
	single := `<cpu mode='custom'/>`
	double := `<cpu mode="custom"/>`
	if !containsEdit(single, "cpu_mode", "custom") {
		t.Error("containsEdit missed single-quoted cpu_mode")
	}
	if !containsEdit(double, "cpu_mode", "custom") {
		t.Error("containsEdit missed double-quoted cpu_mode")
	}
}
