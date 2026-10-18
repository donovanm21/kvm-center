//go:build unix

package nativevalidation

import (
	"errors"
	"fmt"
	"kvm-center/internal/nativework"
	"os"
	"path/filepath"
	"syscall"
)

// Application configuration, never derived from caller Home/request/journal.
// Tests substitute private synthetic endpoints through package-private seams.
var powerScopeBase = "/var/lib/kvm-center/native-power"
var powerEndpoint = "/run/libvirt/libvirt-sock"

func powerScope(v Virsh, id Identity) (string, error) {
	endpoint, err := filepath.EvalSymlinks(v.Socket)
	if err != nil {
		return "", err
	}
	allowed, err := filepath.EvalSymlinks(powerEndpoint)
	if err != nil || endpoint != allowed {
		return "", errors.New("unsupported native endpoint scope")
	}
	if err = id.Validate(); err != nil {
		return "", err
	}
	root := filepath.Join(powerScopeBase, hash(endpoint), id.UUID)
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	for _, p := range []string{powerScopeBase, filepath.Dir(root), root} {
		s, e := os.Lstat(p)
		if e != nil || !s.IsDir() || s.Mode().Perm()&0077 != 0 || s.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("private canonical power scope required")
		}
	}
	// Sync the full ancestor chain, including parents of newly created entries.
	for p := root; ; p = filepath.Dir(p) {
		if err := uiSync(p); err != nil {
			return "", err
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return root, nil
}
func powerLock(root string) (*os.File, error) {
	p := filepath.Join(root, "writer.lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("invalid application writer lock")
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: application domain writer busy", nativework.ErrBlocked)
	}
	return f, nil
}
