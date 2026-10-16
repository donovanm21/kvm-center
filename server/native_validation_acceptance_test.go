//go:build nativevalidation && linux

package main

import (
	"context"
	"kvm-center/internal/nativevalidation"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Excluded from normal CI. This test can mutate ONE explicitly approved guest
// through an existing local socket, only with both opt-in and manifest values.
// It does not start production agent/server main or enroll a live host.
func TestNativeValidationAcceptanceOwnedDomain(t *testing.T) {
	if os.Getenv("KVMC_NATIVE_VALIDATE") != "I_APPROVE_ONE_DISPOSABLE_DOMAIN" {
		t.Fatal("explicit native mutation opt-in required")
	}
	id := nativevalidation.Identity{UUID: os.Getenv("KVMC_NATIVE_UUID"), Name: os.Getenv("KVMC_NATIVE_NAME"), Owner: os.Getenv("KVMC_NATIVE_OWNER")}
	if err := id.Validate(); err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("KVMC_NATIVE_ROOT")
	if !filepath.IsAbs(root) {
		t.Fatal("absolute manifest root required")
	}
	st, err := os.Lstat(root)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 || st.Mode()&os.ModeSymlink != 0 {
		t.Fatal("existing private manifest root required")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("manifest root must be empty")
	}
	state := filepath.Join(root, "agent-state")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	h := newIntegrationHarness(t)
	h.Client.Timeout = 3 * time.Second
	h.registerFirstAdmin("native-validation-admin", "synthetic-native-fixture-password")
	secret := h.Store.GetOrCreateConsoleSecret()
	secretPath := filepath.Join(root, "synthetic-secret")
	if err = os.WriteFile(secretPath, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	// Keep journal/evidence root for explicit postflight review; remove the
	// synthetic secret after the in-process adapter has stopped.
	t.Cleanup(func() { _ = os.Remove(secretPath) })
	driver := nativevalidation.Virsh{Binary: os.Getenv("KVMC_NATIVE_VIRSH"), Socket: os.Getenv("KVMC_NATIVE_SOCKET"), Home: state}
	if err = driver.Validate(); err != nil {
		t.Fatal(err)
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	t.Cleanup(stopSignals) // Registered before adapter cleanup: signals remain caught while cleanup drains.
	ctx, cancel := context.WithCancel(signalCtx)
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- nativevalidation.Run(ctx, nativevalidation.Config{OptIn: true, Identity: id, StateDir: state, SecretFile: secretPath, Listen: "127.0.0.1:0", Runtime: nativevalidation.MaxRuntime}, driver, func(addr string) { ready <- addr })
	}()
	t.Cleanup(func() {
		cancel()
		if signalCtx.Err() != nil {
			t.Error("native acceptance interrupted by signal")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("native adapter/cleanup failed: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("adapter cleanup did not finish; preserve manifest and investigate")
		}
	})
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		done <- err
		t.Fatalf("native adapter startup failed: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("native adapter readiness timeout")
	}
	exerciseValidationAPI(t, h, "http://"+addr, id, secret)
}
