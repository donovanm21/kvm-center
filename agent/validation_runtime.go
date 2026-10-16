package main

// The validation entrypoint deliberately never invokes production startup,
// collectors, command handlers, sync workers, self-update or retirement.
// It is a fixture harness, not a native-host isolation boundary.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type validationConfig struct{ stateDir, pkiDir, listen string }

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validationRoot(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("explicit absolute state and PKI directories are required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("validation root must be a private directory, not a symlink (mode 0700)")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(canonical, "?#") {
		return "", errors.New("validation roots cannot contain SQLite URI delimiters")
	}
	for _, original := range []string{"/etc", "/opt/kvm-center", "/var/lib"} {
		resolved, err := filepath.EvalSymlinks(original)
		if err != nil {
			resolved = original
		}
		if pathWithin(resolved, canonical) || pathWithin(canonical, resolved) {
			return "", errors.New("validation root overlaps a host configuration/state directory")
		}
	}
	return canonical, nil
}

func resolveValidationConfig(state, pki, listen string, getenv func(string) string) (validationConfig, error) {
	var cfg validationConfig
	// No production endpoint, credentials, or state override is consumed here.
	// Reject them explicitly rather than leaving inherited settings ambiguous.
	for _, key := range []string{"KVM_AGENT_OPS_DB", "KVM_CENTER_SERVER", "KVM_CENTER_GRPC_ADDR", "KVM_CENTER_SERVER_CA", "KVM_CENTER_AGENT_PORT"} {
		if getenv(key) != "" {
			return cfg, fmt.Errorf("validation does not accept inherited %s", key)
		}
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return cfg, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return cfg, errors.New("validation listener must use a literal loopback IP")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return cfg, errors.New("invalid validation port")
	}
	cfg.stateDir, err = validationRoot(state)
	if err != nil {
		return cfg, err
	}
	cfg.pkiDir, err = validationRoot(pki)
	if err != nil {
		return cfg, err
	}
	if pathWithin(cfg.stateDir, cfg.pkiDir) || pathWithin(cfg.pkiDir, cfg.stateDir) {
		return cfg, errors.New("state and PKI roots must be separate, non-overlapping directories")
	}
	entries, err := os.ReadDir(cfg.pkiDir)
	if err != nil {
		return cfg, err
	}
	if len(entries) != 0 {
		return cfg, errors.New("validation v1 requires an empty PKI directory; credentials are unsupported")
	}
	cfg.listen = net.JoinHostPort(ip.String(), strconv.Itoa(n))
	return cfg, nil
}

func validationMux(addr string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "mode": "validation", "fixture": true, "listen": addr, "version": Version})
	})
	mux.HandleFunc("/validation/inventory", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"fixture": true, "host_id": "validation-fixture", "vms": []any{}, "storage": []any{}, "networks": []any{}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unsupported in fixture-only validation mode", http.StatusNotImplemented)
	})
	return mux
}

func runValidationAgent(ctx context.Context, cfg validationConfig, ready func(string)) error {
	// Recheck roots at the last boundary before opening state. Roots are owned
	// exclusively by the test runner; concurrent external edits are unsupported.
	checked, err := resolveValidationConfig(cfg.stateDir, cfg.pkiDir, cfg.listen, func(string) string { return "" })
	if err != nil {
		return err
	}
	cfg = checked
	lockPath := filepath.Join(cfg.stateDir, "validation.lock")
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("acquire validation state ownership (remove stale lock only after verifying its process stopped): %w", err)
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(lockPath)
		return err
	}
	defer os.Remove(lockPath)
	entries, err := os.ReadDir(cfg.stateDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		switch e.Name() {
		case "validation.lock", "operations.db", "operations.db-wal", "operations.db-shm":
		default:
			return fmt.Errorf("unexpected validation state entry %q", e.Name())
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("validation state entry %q must be a regular file", e.Name())
		}
	}
	store, err := OpenOperationsStore(filepath.Join(cfg.stateDir, "operations.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	// Never reconcile stored rows against native libvirt. This DB is reserved
	// for fixture operations and is not assigned to the production global ops.
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() {
		return errors.New("validation listener did not bind loopback")
	}
	srv := &http.Server{Handler: validationMux(listener.Addr().String()), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	if ready != nil {
		ready(listener.Addr().String())
	}
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		if err != nil {
			_ = srv.Close()
		}
		serveErr := <-done
		if err != nil {
			return err
		}
		if !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}
