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
	"strings"
	"time"
)

// Virsh is restricted to an existing local Unix socket and an explicit binary.
// It never uses default URI, SSH, credential helpers or the caller's environment.
type Virsh struct{ Binary, Socket, Home string }

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

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("virsh output exceeded limit")
	}
	return b.Buffer.Write(p)
}
func (v Virsh) run(ctx context.Context, input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	uri := "qemu+unix:///system?" + url.Values{"socket": []string{v.Socket}}.Encode()
	argv := append([]string{"--connect", uri}, args...)
	cmd := exec.CommandContext(ctx, v.Binary, argv...)
	cmd.Env = []string{"LC_ALL=C", "LANG=C", "HOME=" + v.Home}
	cmd.WaitDelay = time.Second
	cmd.Stdin = strings.NewReader(input)
	var output limitedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if strings.Contains(output.String(), "Domain not found:") {
			return "", ErrAbsent
		}
		return "", fmt.Errorf("virsh %s failed (private output omitted): %w", args[0], err)
	}
	return strings.TrimSpace(output.String()), nil
}
func (v Virsh) XML(ctx context.Context, target string) (string, error) {
	return v.run(ctx, "", "dumpxml", target, "--inactive")
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
