package nativevalidation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Virsh is restricted to an existing local Unix socket and an explicit binary.
// It never uses default URI, SSH, credential helpers or the caller's environment.
type Virsh struct {
	Binary, Socket, Home string
	mutation             *VirshMutation
}

func (v Virsh) Validate() error {
	for _, p := range []string{v.Binary, v.Socket, v.Home} {
		if !filepath.IsAbs(p) {
			return errors.New("absolute virsh, socket and private state paths required")
		}
	}
	b, err := os.Stat(v.Binary)
	if err != nil {
		return err
	}
	if !b.Mode().IsRegular() || b.Mode()&0111 == 0 {
		return errors.New("virsh must be an existing executable")
	}
	s, err := os.Stat(v.Socket)
	if err != nil {
		return err
	}
	if s.Mode()&os.ModeSocket == 0 {
		return errors.New("libvirt endpoint must be an existing Unix socket")
	}
	return nil
}

type limitedOutput struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *limitedOutput) Len() int       { return b.buffer.Len() }
func (b *limitedOutput) String() string { return b.buffer.String() }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		b.exceeded = true
		return 0, errors.New("virsh output exceeded limit")
	}
	return b.buffer.Write(p)
}
func (v Virsh) runRaw(ctx context.Context, input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	uri := "qemu+unix:///system?" + url.Values{"socket": []string{v.Socket}}.Encode()
	argv := append([]string{"--connect", uri}, args...)
	cmd := exec.CommandContext(ctx, v.Binary, argv...)
	cmd.Env = []string{"LC_ALL=C", "LANG=C", "HOME=" + v.Home}
	cmd.WaitDelay = time.Second
	cmd.Stdin = strings.NewReader(input)
	var output, diagnostic limitedOutput
	cmd.Stdout = &output
	cmd.Stderr = &diagnostic
	var err error
	switch args[0] {
	case "define", "start", "destroy", "undefine":
		if v.mutation == nil {
			return "", ErrMutationTrackingRequired
		}
		err = v.mutation.run(ctx, v, cmd, input, args)
	default:
		err = cmd.Run()
	}
	if output.exceeded || diagnostic.exceeded {
		return "", errors.New("virsh output exceeded limit")
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("virsh %s failed (private output omitted): %w", args[0], err)
	}
	if diagnostic.Len() != 0 {
		return "", errors.New("virsh emitted diagnostics (private output omitted)")
	}
	return output.String(), nil
}
func (v Virsh) run(ctx context.Context, input string, args ...string) (string, error) {
	output, err := v.runRaw(ctx, input, args...)
	return strings.TrimSpace(output), err
}

var inventoryUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// inventoryContains requires the complete machine-readable list from the
// authorized local connection. Identifiers are never logged or persisted.
// This proves absence only with unfiltered inventory visibility and exclusive
// fixture ownership; it cannot reserve a UUID/name against concurrent changes.
func inventoryContains(output, target string, uuid bool) (bool, error) {
	if !utf8.ValidString(output) {
		return false, errors.New("invalid domain inventory encoding")
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return false, nil
	}
	seen := map[string]bool{}
	found := false
	for _, line := range lines {
		if line == "" || strings.TrimSpace(line) != line || strings.IndexFunc(line, unicode.IsControl) >= 0 || (uuid && !inventoryUUID.MatchString(line)) || seen[line] {
			return false, errors.New("ambiguous domain inventory")
		}
		seen[line] = true
		if line == target {
			found = true
		}
	}
	return found, nil
}

func (v Virsh) XML(ctx context.Context, target string) (string, error) {
	uuid := uuidPattern.MatchString(target)
	if !uuid && !namePattern.MatchString(target) {
		return "", errors.New("exact validation UUID or name required")
	}
	raw, lookupErr := v.run(ctx, "", "dumpxml", target, "--inactive")
	if lookupErr == nil {
		return raw, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var exitErr *exec.ExitError
	if !errors.As(lookupErr, &exitErr) {
		return "", lookupErr
	}
	// Error wording varies by installed virsh. Never infer absence from it.
	// A failed read may be classified absent only by a separately successful
	// full inventory read; connection/permission/protocol failures stay closed.
	flag := "--name"
	if uuid {
		flag = "--uuid"
	}
	inventory, err := v.runRaw(ctx, "", "list", "--all", flag)
	if err != nil {
		return "", fmt.Errorf("domain absence verification failed: %w", err)
	}
	present, err := inventoryContains(inventory, target, uuid)
	if err != nil {
		return "", err
	}
	if present {
		return "", lookupErr
	}
	return "", ErrAbsent
}
func (v Virsh) Define(ctx context.Context, raw string) error {
	_, err := v.run(ctx, raw, "define", "/dev/stdin")
	return err
}
func (v Virsh) State(ctx context.Context, id string) (string, error) {
	info, err := v.run(ctx, "", "dominfo", id)
	if err != nil {
		return "", err
	}
	disabled := false
	for _, line := range strings.Split(info, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "Autostart" && strings.TrimSpace(value) == "disable" {
			disabled = true
		}
	}
	if !disabled {
		return "", errors.New("owned domain autostart must remain disabled")
	}
	return v.run(ctx, "", "domstate", id)
}
func (v Virsh) Start(ctx context.Context, id string) error {
	_, err := v.run(ctx, "", "start", id)
	return err
}
func (v Virsh) Destroy(ctx context.Context, id string) error {
	_, err := v.run(ctx, "", "destroy", id)
	return err
}
func (v Virsh) Undefine(ctx context.Context, id string) error {
	_, err := v.run(ctx, "", "undefine", id)
	return err
}

// ReadPrivate rejects symlinks and oversized files. Paths are exclusively owned
// by the runner; concurrent modification of the private root is unsupported.
func ReadPrivate(path string) ([]byte, error) {
	s, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !s.Mode().IsRegular() || s.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private regular file required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if len(b) > 1<<20 {
		return nil, errors.New("private file too large")
	}
	return b, err
}
