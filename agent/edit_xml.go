package main

// applyXMLEdits (v5.27.4) mutates a shut-off VM's domain XML to apply
// field changes that setvcpus / setmem / autostart can't carry:
//   cpu_mode     → <cpu mode='...'>
//   video_model  → <video><model type='...'/></video> (first adapter)
//   boot_dev     → <os><boot dev='...'/>
//
// We dump the current XML, patch the relevant elements, and
// re-define the domain. Caller must have already confirmed the VM is
// not running (virsh define will refuse on a live domain with a
// definition change that would require reboot).

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// dominXML is a minimal subset of the domain schema — just enough to
// round-trip the fields we want to edit. Anything we don't declare
// here is preserved via RawXML capture on each element the user can
// mutate. We avoid full-schema parsing because domain XML has
// hundreds of optional fields and encoding/xml's round-trip is lossy
// for namespaced attributes libvirt emits.

// patchDomainXML is a string-level editor — we regex (or simple
// string) the specific attribute values and rewrite. This is
// intentionally conservative: grep for the element, replace only
// inside its opening tag. libvirt accepts the updated XML on
// `virsh define` the same way it accepts a hand-edited one.
func patchDomainXML(in string, edits map[string]string) (string, error) {
	out := in
	for key, val := range edits {
		switch key {
		case "cpu_mode":
			// <cpu mode='X'> may also carry match='...' or check='...'.
			// Only touch the mode attribute; leave siblings intact.
			newOut, err := replaceTagAttr(out, "cpu", "mode", val)
			if err != nil {
				return "", fmt.Errorf("cpu_mode patch: %w", err)
			}
			out = newOut
		case "boot_dev":
			// <boot dev='hd'/> lives under <os>. There can be multiple
			// <boot> elements (boot order); we replace the FIRST one
			// which is historically the primary boot device.
			newOut, err := replaceTagAttrFirst(out, "boot", "dev", val)
			if err != nil {
				return "", fmt.Errorf("boot_dev patch: %w", err)
			}
			out = newOut
		case "video_model":
			// <video><model type='X'.../></video>. Replace the model
			// type attr on the first <model> inside a <video> block.
			newOut, err := replaceVideoModel(out, val)
			if err != nil {
				return "", fmt.Errorf("video_model patch: %w", err)
			}
			out = newOut
		default:
			return "", fmt.Errorf("unsupported XML edit key: %s", key)
		}
	}
	return out, nil
}

// replaceTagAttr rewrites the `attr` of the first <tag ...>
// occurrence to `val`. Returns the original string + error if the
// tag+attr wasn't found.
func replaceTagAttr(in, tag, attr, val string) (string, error) {
	return replaceTagAttrN(in, tag, attr, val, -1)
}

func replaceTagAttrFirst(in, tag, attr, val string) (string, error) {
	return replaceTagAttrN(in, tag, attr, val, 1)
}

// replaceTagAttrN walks the string, finds each `<tag` opening,
// and within its attribute list replaces attr='...' (or attr="...").
// N limits how many occurrences to touch; -1 = all.
func replaceTagAttrN(in, tag, attr, val string, n int) (string, error) {
	start := 0
	hits := 0
	out := in
	for {
		idx := strings.Index(out[start:], "<"+tag)
		if idx < 0 {
			break
		}
		abs := start + idx
		// Find the end of this opening tag: first '>' after abs.
		close := strings.IndexAny(out[abs:], ">")
		if close < 0 {
			break
		}
		endTag := abs + close
		// Attribute list is inside [abs+len("<"+tag), endTag).
		attrs := out[abs+len("<"+tag) : endTag]
		// Only treat this as a match if the next rune is whitespace
		// or the closing bracket — avoids <cputune> being matched as <cpu>.
		if len(attrs) > 0 && attrs[0] != ' ' && attrs[0] != '\t' && attrs[0] != '\n' && attrs[0] != '>' && attrs[0] != '/' {
			start = endTag
			continue
		}
		// Replace attr='...' or attr="..." within attrs.
		newAttrs, replaced := replaceOneAttr(attrs, attr, val)
		if replaced {
			out = out[:abs+len("<"+tag)] + newAttrs + out[endTag:]
			endTag = abs + len("<"+tag) + len(newAttrs)
			hits++
			if n > 0 && hits >= n {
				return out, nil
			}
		}
		start = endTag
	}
	if hits == 0 {
		return in, fmt.Errorf("tag <%s> with %s attr not found", tag, attr)
	}
	return out, nil
}

// replaceOneAttr updates attr=VALUE within an attribute list.
// Returns (newAttrs, replaced).
func replaceOneAttr(attrs, attr, val string) (string, bool) {
	for _, q := range []byte{'\'', '"'} {
		needle := attr + "=" + string(q)
		i := strings.Index(attrs, needle)
		if i < 0 {
			continue
		}
		endQ := strings.IndexByte(attrs[i+len(needle):], q)
		if endQ < 0 {
			continue
		}
		return attrs[:i+len(needle)] + val + attrs[i+len(needle)+endQ:], true
	}
	return attrs, false
}

// replaceVideoModel rewrites the first <model type='...'/> found
// inside a <video>...</video> block. This is targeted enough to
// avoid colliding with <cpu><model>...</model></cpu>.
func replaceVideoModel(in, newType string) (string, error) {
	// Find <video> ... </video>. We accept <video/> (self-closing)
	// but nothing inside it means no <model> to patch.
	vOpen := strings.Index(in, "<video")
	if vOpen < 0 {
		return in, fmt.Errorf("no <video> element in domain XML")
	}
	vClose := strings.Index(in[vOpen:], "</video>")
	if vClose < 0 {
		// Self-closing or missing — libvirt may emit <video/>; caller
		// must reconfigure via virt-install + re-create.
		return in, fmt.Errorf("no </video> closing tag — cannot patch self-closing video element")
	}
	vEnd := vOpen + vClose
	block := in[vOpen:vEnd]
	patched, err := replaceTagAttrFirst(block, "model", "type", newType)
	if err != nil {
		return in, err
	}
	return in[:vOpen] + patched + in[vEnd:], nil
}

// applyXMLEdits: dumpxml → patchDomainXML → virsh define. Uses a tmp
// file for the define input (virsh define accepts a path).
func applyXMLEdits(vmTarget string, edits map[string]string) error {
	// Use --inactive so we edit the persisted config, not the live
	// snapshot. Both forms are the same for a shut-off VM anyway.
	raw, err := exec.Command("virsh", "dumpxml", "--inactive", vmTarget).Output()
	if err != nil {
		return fmt.Errorf("dumpxml: %w", err)
	}
	patched, err := patchDomainXML(string(raw), edits)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "kvmc-edit-*.xml")
	if err != nil {
		return fmt.Errorf("tmp create: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(patched); err != nil {
		tmp.Close()
		return fmt.Errorf("tmp write: %w", err)
	}
	tmp.Close()

	if o, e := exec.Command("virsh", "define", tmp.Name()).CombinedOutput(); e != nil {
		return fmt.Errorf("virsh define: %s", strings.TrimSpace(string(o)))
	}

	// Sanity-check: dumpxml round-trip preserves every edit we just
	// applied. Guards against libvirt silently rewriting an element
	// back to its prior shape.
	if out2, _ := exec.Command("virsh", "dumpxml", "--inactive", vmTarget).Output(); len(out2) > 0 {
		for k, v := range edits {
			if !containsEdit(string(out2), k, v) {
				return fmt.Errorf("libvirt did not preserve %s=%s after define — check domain XML manually", k, v)
			}
		}
	}
	return nil
}

// containsEdit performs a loose post-define sanity check that the
// edited field still carries the target value in the domain XML.
func containsEdit(xmlStr, key, val string) bool {
	switch key {
	case "cpu_mode":
		return strings.Contains(xmlStr, "mode='"+val+"'") || strings.Contains(xmlStr, `mode="`+val+`"`)
	case "boot_dev":
		return strings.Contains(xmlStr, "dev='"+val+"'") || strings.Contains(xmlStr, `dev="`+val+`"`)
	case "video_model":
		return strings.Contains(xmlStr, "type='"+val+"'") || strings.Contains(xmlStr, `type="`+val+`"`)
	}
	return true
}

// _ = xml.Unmarshal keeps the import in case a future patch needs
// structured parsing — avoids "imported but not used" on some tool
// chains if the string-level editor is later replaced.
var _ = xml.Unmarshal
