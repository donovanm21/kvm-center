package main

import (
	"bufio"
	"context"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validationDirs(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	state, pki := filepath.Join(root, "state"), filepath.Join(root, "pki")
	for _, p := range []string{state, pki} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return state, pki
}
func emptyValidationEnv(string) string { return "" }
func TestValidationConfig(t *testing.T) {
	state, pki := validationDirs(t)
	for _, addr := range []string{"127.0.0.1:0", "[::1]:8082"} {
		if _, err := resolveValidationConfig(state, pki, addr, emptyValidationEnv); err != nil {
			t.Fatal(err)
		}
	}
	for _, addr := range []string{":8082", "0.0.0.0:0", "[::]:0", "localhost:8082", "192.0.2.1:0", "127.0.0.1:-1", "127.0.0.1:65536"} {
		if _, err := resolveValidationConfig(state, pki, addr, emptyValidationEnv); err == nil {
			t.Fatalf("accepted %s", addr)
		}
	}
	for _, key := range []string{"KVM_AGENT_OPS_DB", "KVM_CENTER_SERVER", "KVM_CENTER_GRPC_ADDR", "KVM_CENTER_SERVER_CA", "KVM_CENTER_AGENT_PORT"} {
		if _, err := resolveValidationConfig(state, pki, "127.0.0.1:0", func(k string) string {
			if k == key {
				return "inherited"
			}
			return ""
		}); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	for _, name := range []string{"state?one", "state#one"} {
		path := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveValidationConfig(path, pki, "127.0.0.1:0", emptyValidationEnv); err == nil {
			t.Fatal("accepted SQLite URI delimiter")
		}
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatal("invalid root was written")
		}
	}
	if _, err := resolveValidationConfig(state, state, "127.0.0.1:0", emptyValidationEnv); err == nil {
		t.Fatal("accepted overlapping roots")
	}
	if _, err := resolveValidationConfig("", pki, "127.0.0.1:0", emptyValidationEnv); err == nil {
		t.Fatal("accepted missing root")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(state, link); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveValidationConfig(link, pki, "127.0.0.1:0", emptyValidationEnv); err == nil {
		t.Fatal("accepted symlink root")
	}
	if err := os.WriteFile(filepath.Join(pki, "existing.pem"), []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveValidationConfig(state, pki, "127.0.0.1:0", emptyValidationEnv); err == nil {
		t.Fatal("accepted existing PKI")
	}
}
func TestValidationRoutesExcludeNativeHandlers(t *testing.T) {
	handler := validationMux("127.0.0.1:1234")
	for _, path := range []string{"/command", "/command/cancel", "/self-update", "/storage-upload", "/storage-file", "/storage-browse", "/pool-define", "/pool/example/start", "/vm-create", "/console/example", "/ws/example", "/screenshot/example", "/prepare-grant", "/grant-active", "/refresh-telemetry"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			r := httptest.NewRequest(method, path, strings.NewReader(`{"action":"RETIRE","params":{"remove_files":"true"}}`))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusNotImplemented {
				t.Fatalf("%s %s: %d", method, path, w.Code)
			}
		}
	}
	for _, path := range []string{"/health", "/validation/inventory"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 405 {
			t.Fatalf("mutable fixture %s", path)
		}
	}
}
func TestValidationStateFailureBeforeListener(t *testing.T) {
	for _, kind := range []string{"symlink", "corrupt", "unexpected"} {
		t.Run(kind, func(t *testing.T) {
			state, pki := validationDirs(t)
			cfg, err := resolveValidationConfig(state, pki, "127.0.0.1:0", emptyValidationEnv)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(t.TempDir(), "original")
			if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				err = os.Symlink(sentinel, filepath.Join(state, "operations.db"))
			case "corrupt":
				err = os.WriteFile(filepath.Join(state, "operations.db"), []byte("not sqlite"), 0600)
			default:
				err = os.WriteFile(filepath.Join(state, "other-state"), []byte("sentinel"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			ready := false
			if err := runValidationAgent(context.Background(), cfg, func(string) { ready = true }); err == nil {
				t.Fatal("accepted invalid state")
			}
			if ready {
				t.Fatal("listener started before state validated")
			}
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != "unchanged" {
				t.Fatal("original changed")
			}
			if _, err := os.Stat(filepath.Join(state, "validation.lock")); !os.IsNotExist(err) {
				t.Fatal("failed startup retained lock")
			}
		})
	}
}
func TestValidationRuntimeOwnershipAndShutdown(t *testing.T) {
	state, pki := validationDirs(t)
	cfg, err := resolveValidationConfig(state, pki, "127.0.0.1:0", emptyValidationEnv)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- runValidationAgent(ctx, cfg, func(addr string) { ready <- addr }) }()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("startup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("startup timeout")
	}
	if err := runValidationAgent(context.Background(), cfg, nil); err == nil {
		t.Fatal("second owner accepted")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), addr) {
		t.Fatalf("actual bound address missing: %s", body)
	}
	host, port, _ := net.SplitHostPort(addr)
	if !net.ParseIP(host).IsLoopback() || port == "0" {
		t.Fatal("listener not actual loopback")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timeout")
	}
	if _, err := client.Get("http://" + addr + "/health"); err == nil {
		t.Fatal("listener survived shutdown")
	}
	if _, err := os.Stat(filepath.Join(state, "validation.lock")); !os.IsNotExist(err) {
		t.Fatal("lock survived shutdown")
	}
	reopened, err := OpenOperationsStore(filepath.Join(state, "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	entries, err := os.ReadDir(pki)
	if err != nil || len(entries) != 0 {
		t.Fatal("PKI modified")
	}
}

// Exercise the real main early-return boundary in a child test process.
func TestValidationChild(t *testing.T) {
	if os.Getenv("KVMC_VALIDATION_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"agent"}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("agent", flag.ExitOnError)
	main()
	os.Exit(0)
}
func TestValidationMainIsFixtureOnly(t *testing.T) {
	state, pki := validationDirs(t)
	childCtx, cancelChild := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelChild()
	cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestValidationChild$", "--", "-validation", "-validation-state-dir", state, "-validation-pki-dir", pki)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "KVM_") && !strings.HasPrefix(entry, "KVMC_VALIDATION_CHILD=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "KVMC_VALIDATION_CHILD=1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cancelChild()
		_ = cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("child did not exit after kill")
		}
	})
	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-childCtx.Done():
				return
			}
		}
		close(lines)
	}()
	var addr string
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for addr == "" {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("child ended before ready")
			}
			if _, after, ok := strings.Cut(line, "validation fixture listening on "); ok {
				addr = after
			} else {
				t.Fatalf("unexpected production startup output: %s", line)
			}
		case <-timer.C:
			t.Fatal("child startup timeout")
		}
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Post("http://"+addr+"/command", "application/json", strings.NewReader(`{"action":"RETIRE"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 501 {
		t.Fatal("native command registered")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
		if waitErr != nil {
			t.Fatalf("child shutdown: %v", waitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child shutdown timeout")
	}
}
