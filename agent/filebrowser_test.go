package main

import (
	"path/filepath"
	"testing"
)

// TestPathWithinAllowedRoots pins the storage-browser jail. The browser
// must only ever list directories that are equal to, or nested under,
// one of the allowed roots (the active libvirt pool target paths plus a
// sane default set). Everything else — `..` climbs above a root,
// absolute paths into the rest of the filesystem, and sibling
// directories whose names merely *prefix*-match a root — must be
// rejected so a caller who reaches the agent cannot enumerate the host.
func TestPathWithinAllowedRoots(t *testing.T) {
	roots := []string{
		"/var/lib/libvirt/images",
		"/var/lib/libvirt/boot",
		"/srv/pool/fast", // stand-in for a real pool target path
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		// The roots themselves are browsable.
		{"root images itself", "/var/lib/libvirt/images", true},
		{"root boot itself", "/var/lib/libvirt/boot", true},
		{"derived pool root itself", "/srv/pool/fast", true},

		// Children of a root are browsable.
		{"child dir of images", "/var/lib/libvirt/images/templates", true},
		{"nested child of images", "/var/lib/libvirt/images/a/b/c", true},
		{"child of derived pool", "/srv/pool/fast/iso", true},

		// A trailing slash on an allowed path is still in-jail.
		{"root with trailing slash", "/var/lib/libvirt/images/", true},

		// Cleanable `..` that stays inside a root is fine.
		{"dotdot that stays inside", "/var/lib/libvirt/images/x/../templates", true},

		// `..` traversal that climbs above a root is rejected.
		{"dotdot escapes to libvirt parent", "/var/lib/libvirt/images/..", false},
		{"dotdot escapes to root", "/var/lib/libvirt/images/../../../../etc", false},

		// Absolute paths outside every root are rejected.
		{"etc", "/etc", false},
		{"etc passwd", "/etc/passwd", false},
		{"root home", "/root", false},
		{"filesystem root", "/", false},
		{"libvirt parent (not a root)", "/var/lib/libvirt", false},

		// Separator-aware prefix check: a sibling whose name merely
		// begins with a root must NOT match.
		{"prefix-collision sibling", "/var/lib/libvirt/images-evil", false},
		{"prefix-collision sibling child", "/var/lib/libvirt/images-evil/secret", false},
		{"derived pool prefix collision", "/srv/pool/fast-evil", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathWithinAllowedRoots(tc.path, roots); got != tc.want {
				t.Fatalf("pathWithinAllowedRoots(%q, roots) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestPathWithinAllowedRootsEmpty makes sure a misconfigured / empty
// root set is fail-closed: with no allowed roots, nothing is browsable.
func TestPathWithinAllowedRootsEmpty(t *testing.T) {
	if pathWithinAllowedRoots("/var/lib/libvirt/images", nil) {
		t.Fatal("empty root set must reject every path (fail closed)")
	}
}

// TestStorageBrowseAllowedRootsHasDefault guarantees the default root
// set always contains the canonical libvirt image dir, so a host whose
// pools haven't been enumerated yet (or whose virsh call failed) still
// has at least one safe place to browse rather than defaulting to `/`.
func TestStorageBrowseAllowedRootsHasDefault(t *testing.T) {
	roots := storageBrowseAllowedRoots()
	found := false
	for _, r := range roots {
		if r == filepath.Clean("/var/lib/libvirt/images") {
			found = true
		}
		if !filepath.IsAbs(r) {
			t.Fatalf("allowed root %q is not absolute", r)
		}
	}
	if !found {
		t.Fatalf("default allowed roots %v missing /var/lib/libvirt/images", roots)
	}
}
