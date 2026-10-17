package nativevalidation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const MaxRuntime = 45 * time.Second
const CleanupBudget = 15 * time.Second

type Config struct {
	OptIn, CleanupOnly, CleanupStopped bool
	Identity                           Identity
	StateDir, SecretFile, Listen       string
	Runtime                            time.Duration
}

func privateRoot(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("absolute private state directory required")
	}
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 || st.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("state root must be a private non-symlink directory")
	}
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	for _, blocked := range []string{"/etc", "/var/lib", "/opt/kvm-center"} {
		if resolved, err := filepath.EvalSymlinks(blocked); err == nil {
			blocked = resolved
		}
		if root == blocked || strings.HasPrefix(root, blocked+"/") || strings.HasPrefix(blocked, root+"/") {
			return "", errors.New("state root overlaps production paths")
		}
	}
	return root, nil
}
func saveJournal(root string, j Journal) error {
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "journal.tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), filepath.Join(root, "journal.json")); err != nil {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func Run(ctx context.Context, c Config, d Driver, ready func(string)) (result error) {
	if c.CleanupStopped && !c.CleanupOnly {
		return errors.New("stopped-only recovery requires cleanup mode")
	}
	if !c.OptIn {
		return errors.New("explicit disposable-libvirt opt-in required")
	}
	if err := c.Identity.Validate(); err != nil {
		return err
	}
	if c.Runtime <= 0 || c.Runtime > MaxRuntime {
		return errors.New("runtime must be within (0,45s]")
	}
	root, err := privateRoot(c.StateDir)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || port == "8081" {
		return errors.New("separate literal loopback listener required; production port 8081 forbidden")
	}
	lock, err := os.OpenFile(filepath.Join(root, "owner.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("state locked: verify old process stopped before explicit stale-lock removal")
	}
	fmt.Fprintln(lock, os.Getpid())
	lock.Close()
	defer os.Remove(filepath.Join(root, "owner.lock"))
	save := func(j Journal) error { return saveJournal(root, j) }
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != "owner.lock" && !(c.CleanupOnly && (e.Name() == "journal.json" || e.Name() == "journal.tmp")) {
			return errors.New("fresh empty state root required (cleanup accepts only journal.json and an interrupted journal.tmp)")
		}
	}
	if c.CleanupOnly {
		raw, err := ReadPrivate(filepath.Join(root, "journal.json"))
		if err != nil {
			return err
		}
		var j Journal
		if err = json.Unmarshal(raw, &j); err != nil {
			return err
		}
		if j.Identity != c.Identity {
			return errors.New("cleanup identity does not match journal")
		}
		if c.CleanupStopped {
			if err = stoppedRecoveryJournal(j); err != nil {
				return err
			}
		}
		// Only a valid committed journal can authorize discarding an uncommitted
		// partial update. Never follow a symlink or use temp contents as ownership.
		temp := filepath.Join(root, "journal.tmp")
		if _, err := os.Lstat(temp); err == nil {
			if _, err = ReadPrivate(temp); err != nil {
				return err
			}
			if err = os.Remove(temp); err != nil {
				return err
			}
			dir, err := os.Open(root)
			if err != nil {
				return err
			}
			err = dir.Sync()
			dir.Close()
			if err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		a, err := Recover(d, j, save)
		if err != nil {
			return err
		}
		cleanup, cancel := context.WithTimeout(context.Background(), CleanupBudget)
		defer cancel()
		if c.CleanupStopped {
			return a.CleanupStopped(cleanup)
		}
		return a.Cleanup(cleanup)
	}
	secret, err := ReadPrivate(c.SecretFile)
	if err != nil {
		return err
	}
	if len(secret) < 32 || len(secret) > 256 {
		return errors.New("synthetic secret must be 32 to 256 bytes")
	}
	// Bind before defining so listener collisions cannot leave a domain.
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	run, cancel := context.WithTimeout(ctx, c.Runtime)
	defer cancel()
	a, err := Prepare(run, d, c.Identity, string(secret), save)
	if a != nil {
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), CleanupBudget)
			defer stop()
			result = errors.Join(result, a.failure(), a.Cleanup(cleanup))
		}()
	}
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: a.Handler(cancel), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 4096, BaseContext: func(net.Listener) context.Context { return run }}
	defer func() { cancel(); _ = srv.Close() }()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	if ready != nil {
		ready(listener.Addr().String())
	}
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-run.Done():
	}
	// Stop admission/cancel handlers before serial cleanup; bounded native calls
	// and the mutex drain any in-flight action before ownership-checked removal.
	_ = srv.Close()
	cancel()
	return nil
}
