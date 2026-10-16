package nativevalidation

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
func TestVirshAbsenceRequiresSuccessfulInventory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX synthetic executable")
	}
	for _, tc := range []struct {
		name, target, lookup, inventory string
		absent                          bool
	}{
		{"installed wording", testID.UUID, "printf 'failed to get domain' >&2; exit 1", "exit 0", true},
		{"older wording", testID.UUID, "printf 'Domain not found:' >&2; exit 1", "exit 0", true},
		{"name lookup", testID.Name, "exit 1", "exit 0", true},
		{"existing UUID", testID.UUID, "exit 1", "printf '%s\\n' '" + testID.UUID + "'", false},
		{"existing name", testID.Name, "exit 1", "printf '%s\\n' '" + testID.Name + "'", false},
		{"other UUID", testID.UUID, "exit 1", "printf '00000000-0000-1000-8000-000000000001\\n'", true},
		{"connection failure", testID.UUID, "exit 1", "printf 'connection refused' >&2; exit 1", false},
		{"permission failure", testID.UUID, "exit 1", "printf 'permission denied' >&2; exit 1", false},
		{"protocol failure", testID.UUID, "exit 1", "printf 'protocol error' >&2; exit 1", false},
		{"warning even on success", testID.UUID, "exit 1", "printf 'partial results' >&2; exit 0", false},
		{"malformed inventory", testID.UUID, "exit 1", "printf 'garbage\\n'", false},
		{"ambiguous name", testID.Name, "exit 1", "printf ' padded-name\\n'", false},
		{"successful lookup diagnostics", testID.UUID, "printf '<domain/>'; printf 'warning' >&2; exit 0", "exit 0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateDir(t)
			binary := filepath.Join(root, "fake-virsh")
			script := "#!/bin/sh\nprintf '%s\\n' \"$3\" >> \"$HOME/calls\"\ncase \"$3\" in\ndumpxml) " + tc.lookup + " ;;\nlist) [ \"$4\" = --all ] || exit 9; "
			flag := "--name"
			if tc.target == testID.UUID {
				flag = "--uuid"
			}
			script += "[ \"$5\" = " + flag + " ] || exit 9; " + tc.inventory + " ;;\n*) exit 9 ;;\nesac\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			v := Virsh{Binary: binary, Socket: "/synthetic/socket", Home: root}
			_, err := v.XML(context.Background(), tc.target)
			if err == nil || errors.Is(err, ErrAbsent) != tc.absent {
				t.Fatalf("absence=%v: %v", tc.absent, err)
			}
			if strings.Contains(err.Error(), "partial results") || strings.Contains(err.Error(), "permission denied") {
				t.Fatal("diagnostic exposed")
			}
			calls, e := os.ReadFile(filepath.Join(root, "calls"))
			if e != nil {
				t.Fatal(e)
			}
			want := "dumpxml\nlist\n"
			if tc.name == "successful lookup diagnostics" {
				want = "dumpxml\n"
			}
			if string(calls) != want {
				t.Fatalf("unexpected calls %q", calls)
			}
		})
	}
}

func TestInventoryRejectsAmbiguousOutput(t *testing.T) {
	for _, output := range []string{"invalid", testID.UUID + "\n" + testID.UUID + "\n", "\n" + testID.UUID, " " + testID.UUID, testID.UUID + "\r\n", string([]byte{255})} {
		if _, err := inventoryContains(output, testID.UUID, true); err == nil {
			t.Fatalf("accepted %q", output)
		}
	}
	for _, output := range []string{testID.Name + "\t", testID.Name + "\n\nother", "duplicate\nduplicate"} {
		if _, err := inventoryContains(output, testID.Name, false); err == nil {
			t.Fatalf("accepted %q", output)
		}
	}
	if found, err := inventoryContains("legitimate internal space\n", testID.Name, false); err != nil || found {
		t.Fatal(found, err)
	}
}

func TestOtherCommandsNeverClassifyErrorTextAsAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX synthetic executable")
	}
	root := privateDir(t)
	binary := filepath.Join(root, "fake-virsh")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'Domain not found: failed to get domain' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	v := Virsh{Binary: binary, Socket: "/synthetic/socket", Home: root}
	for _, call := range []func(context.Context, string) error{v.Define, v.Start, v.Destroy, v.Undefine} {
		if err := call(context.Background(), testID.UUID); err == nil || errors.Is(err, ErrAbsent) {
			t.Fatal(err)
		}
	}
	if _, err := v.State(context.Background(), testID.UUID); err == nil || errors.Is(err, ErrAbsent) {
		t.Fatal(err)
	}
}

func TestLookupTimeoutAndOutputOverflowDoNotVerifyAbsence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX synthetic executable")
	}
	for _, tc := range []struct {
		name, body string
		timeout    bool
	}{
		{"timeout", "exec /bin/sleep 10", true},
		{"overflow", "i=0; while [ $i -lt 18000 ]; do printf 'abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz0123456789\\n'; i=$((i+1)); done; exit 1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateDir(t)
			binary := filepath.Join(root, "fake-virsh")
			script := "#!/bin/sh\nprintf '%s\\n' \"$3\" >> \"$HOME/calls\"\ncase \"$3\" in\ndumpxml) " + tc.body + " ;;\nlist) exit 0 ;;\nesac\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if tc.timeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			}
			_, err := (Virsh{Binary: binary, Socket: "/synthetic/socket", Home: root}).XML(ctx, testID.UUID)
			if err == nil || errors.Is(err, ErrAbsent) {
				t.Fatal(err)
			}
			if tc.timeout && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			calls, e := os.ReadFile(filepath.Join(root, "calls"))
			if tc.timeout && errors.Is(e, os.ErrNotExist) {
				return
			}
			if e != nil || string(calls) != "dumpxml\n" {
				t.Fatalf("calls %q: %v", calls, e)
			}
		})
	}
}

func TestOutputLimitAppliesToIOCopy(t *testing.T) {
	var output limitedOutput
	_, err := io.Copy(&output, strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	if err == nil || !output.exceeded || output.Len() > 1<<20 {
		t.Fatal("output cap bypassed", output.Len(), err)
	}
}
