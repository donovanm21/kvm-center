package nativevalidation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVirshUsesOnlyExplicitLocalSocketAndCleanEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX synthetic executable")
	}
	root := privateDir(t)
	binary := filepath.Join(root, "fake-virsh")
	script := `#!/bin/sh
if [ -n "$LIBVIRT_DEFAULT_URI" ] || [ -n "$KVM_CENTER_SERVER" ]; then exit 9; fi
printf '%s\n' "$@" > "$HOME/arguments"
case "$3" in
dumpxml) printf '<domain/>' ;;
dominfo) printf 'Autostart:      disable\n' ;;
domstate) printf 'shut off\n' ;;
*) exit 0 ;;
esac
`
	if e := os.WriteFile(binary, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("LIBVIRT_DEFAULT_URI", "qemu+ssh://unrelated/system")
	t.Setenv("KVM_CENTER_SERVER", "https://unrelated.invalid")
	v := Virsh{Binary: binary, Socket: "/run/libvirt/libvirt-sock", Home: root}
	if _, e := v.XML(context.Background(), testID.UUID); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(root, "arguments"))
	if e != nil {
		t.Fatal(e)
	}
	want := "--connect\nqemu+unix:///system?socket=%2Frun%2Flibvirt%2Flibvirt-sock\ndumpxml\n" + testID.UUID + "\n--inactive\n"
	if string(raw) != want {
		t.Fatalf("unexpected native argv: %q", raw)
	}
	if s, e := v.State(context.Background(), testID.UUID); e != nil || s != "shut off" {
		t.Fatal(s, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := v.XML(ctx, testID.UUID); !errors.Is(e, context.Canceled) {
		t.Fatal("cancel not propagated", e)
	}
}
func TestVirshAbsenceIsNotConnectionFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX synthetic executable")
	}
	for _, message := range []string{"Domain not found: synthetic fixture absent", "Failed to connect socket: permission denied"} {
		root := privateDir(t)
		binary := filepath.Join(root, "fake-virsh")
		if e := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' '"+message+"'\nexit 1\n"), 0700); e != nil {
			t.Fatal(e)
		}
		_, e := (Virsh{Binary: binary, Socket: "/run/libvirt/libvirt-sock", Home: root}).XML(context.Background(), testID.UUID)
		if e == nil || errors.Is(e, ErrAbsent) != strings.HasPrefix(message, "Domain not found:") {
			t.Fatal("incorrect absence classification", e)
		}
	}
}
