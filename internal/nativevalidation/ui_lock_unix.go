//go:build unix

package nativevalidation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// flock is held until process exit, including SIGKILL; recovery never deletes a
// stale lock or guesses from PID reuse. Old/live helper -> LOCK_NB refuses.
func uiLock(root string, recovery bool) (*os.File, error) {
	path := filepath.Join(root, "owner.lock")
	flags := os.O_RDWR | os.O_CREATE | os.O_EXCL
	if recovery {
		if _, e := privateUIFile(path); e != nil {
			return nil, e
		}
		flags = os.O_RDWR
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another fixture helper holds ownership lock")
	}
	if !recovery {
		if _, err = fmt.Fprintln(f, os.Getpid()); err == nil {
			err = f.Sync()
		}
		if err == nil {
			err = uiSync(root)
		}
		if err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}
