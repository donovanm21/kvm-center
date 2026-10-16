package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	agentpb "kvm-center/gen/agentpb"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"kvm-center/internal/models"
)

type drainBackend struct {
	stubBackend
	save     func(context.Context, *persistData) error
	closeErr error
	closes   atomic.Int32
}

func (b *drainBackend) Save(ctx context.Context, d *persistData) error {
	if b.save != nil {
		return b.save(ctx, d)
	}
	return nil
}
func (b *drainBackend) Close(context.Context) error { b.closes.Add(1); return b.closeErr }
func drainStore(b Backend) *Store {
	return &Store{backend: b, saveGateOpen: true, commandLog: NewCommandLog(), roles: map[string]*models.Role{}}
}
func testCoordinator(s *Store) *shutdownCoordinator {
	return &shutdownCoordinator{store: s, cancel: func() {}, restart: make(chan struct{}, 1)}
}
func awaitTest(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for fixture")
	}
}

func TestShutdownInflightAndQueuedPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	real := NewJSONBackend(path)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	b := &drainBackend{}
	b.save = func(ctx context.Context, d *persistData) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return real.Save(ctx, d)
	}
	s := drainStore(b)
	c := testCoordinator(s)
	s.AsyncSave()
	awaitTest(t, entered)
	handlerEntered, handlerRelease, handlerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h := c.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(handlerEntered)
		<-handlerRelease
		s.mu.Lock()
		s.roles["accepted"] = &models.Role{ID: "accepted"}
		s.mu.Unlock()
		s.AsyncSave()
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	go func() { defer close(handlerDone); h.ServeHTTP(response, httptest.NewRequest("POST", "/", nil)) }()
	awaitTest(t, handlerEntered)
	cancelled := make(chan struct{})
	c.cancel = func() { close(cancelled) }
	result := make(chan error, 1)
	go func() { result <- c.drain(context.Background()) }()
	awaitTest(t, cancelled)
	refused := httptest.NewRecorder()
	h.ServeHTTP(refused, httptest.NewRequest("POST", "/", nil))
	if refused.Code != http.StatusServiceUnavailable {
		t.Fatalf("new request status=%d", refused.Code)
	}
	select {
	case err := <-result:
		t.Fatalf("drained before accepted work: %v", err)
	default:
	}
	close(handlerRelease)
	awaitTest(t, handlerDone)
	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("drain stalled")
	}
	if response.Code != http.StatusNoContent || b.closes.Load() != 1 {
		t.Fatal("accepted handler or close incomplete")
	}
	loaded, err := real.Load(context.Background())
	if err != nil || loaded.Roles["accepted"] == nil {
		t.Fatalf("accepted mutation missing after reload: %v", err)
	}
	if calls.Load() < 3 {
		t.Fatalf("queued save or final checkpoint skipped: calls=%d", calls.Load())
	}
	if err := s.SaveChecked(); err == nil {
		t.Fatal("save admitted after seal")
	}
	before := calls.Load()
	s.AsyncSave()
	s.saveWG.Wait()
	if calls.Load() != before {
		t.Fatal("async save admitted after seal")
	}
}

func TestShutdownFailureAndLoadGate(t *testing.T) {
	for _, kind := range []string{"save", "close", "load", "panic"} {
		t.Run(kind, func(t *testing.T) {
			b := &drainBackend{}
			s := drainStore(b)
			switch kind {
			case "save":
				b.save = func(context.Context, *persistData) error { return errors.New("write failed") }
			case "close":
				b.closeErr = errors.New("close failed")
			case "load":
				s.saveGateOpen = false
			case "panic":
				s.goWork("fixture", func() { panic("fixture") })
			}
			if err := testCoordinator(s).drain(context.Background()); err == nil {
				t.Fatal("incomplete drain reported success")
			}
			if kind != "close" && b.closes.Load() != 0 {
				t.Fatal("backend closed despite incomplete drain")
			}
		})
	}
}

func TestShutdownDeadlineAndRepeatedSignal(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "repeated-signal"}[repeated], func(t *testing.T) {
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			b := &drainBackend{save: func(context.Context, *persistData) error {
				close(entered)
				<-release
				defer close(finished)
				return nil
			}}
			c := testCoordinator(drainStore(b))
			signals := make(chan os.Signal, 2)
			signals <- syscall.SIGTERM
			result := make(chan error, 1)
			go func() { result <- c.wait(signals, nil, 200*time.Millisecond) }()
			awaitTest(t, entered)
			if repeated {
				signals <- syscall.SIGINT
			}
			select {
			case err := <-result:
				if err == nil || !strings.Contains(err.Error(), "uncertain") {
					t.Fatalf("missing incomplete report: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("budget not bounded")
			}
			if b.closes.Load() != 0 {
				t.Fatal("closed backend under blocked writer")
			}
			close(release)
			awaitTest(t, finished)
		})
	}
}

func TestShutdownSaveLockDeadline(t *testing.T) {
	b := &drainBackend{}
	s := drainStore(b)
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.saveCheckedContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait: %v", err)
	}
}

func TestShutdownDetachedWorkJoined(t *testing.T) {
	s := drainStore(&drainBackend{})
	entered, release := make(chan struct{}), make(chan struct{})
	s.goWork("parent", func() { s.goWork("child", func() { close(entered); <-release }) })
	awaitTest(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := testCoordinator(s).drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unjoined detached work: %v", err)
	}
	close(release)
	if err := s.work.seal(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownRealHTTP(t *testing.T) {
	b := &drainBackend{}
	s := drainStore(b)
	c := testCoordinator(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.cancel = cancel
	entered := make(chan struct{})
	srv := httptest.NewUnstartedServer(c.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		if err := s.SaveChecked(); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	srv.Config.BaseContext = func(net.Listener) context.Context { return ctx }
	srv.Start()
	defer srv.Close()
	c.servers = []*http.Server{srv.Config}
	requestDone := make(chan error, 1)
	go func() {
		resp, err := srv.Client().Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				err = errors.New("inflight response lost")
			}
		}
		requestDone <- err
	}()
	awaitTest(t, entered)
	budget, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err := c.drain(budget); err != nil {
		t.Fatal(err)
	}
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
}

func TestShutdownChildMain(t *testing.T) {
	if os.Getenv("KVMC_SHUTDOWN_CHILD") != "1" {
		return
	}
	root := os.Getenv("KVMC_SHUTDOWN_ROOT")
	flag.CommandLine = flag.NewFlagSet("shutdown-fixture", flag.ExitOnError)
	os.Args = []string{"shutdown-fixture", "-graceful-shutdown", "-no-tls", "-addr", "127.0.0.1:0", "-data", filepath.Join(root, "state.json"), "-ca-dir", filepath.Join(root, "ca")}
	main()
	os.Exit(0)
}

func TestShutdownRealProcess(t *testing.T) {
	// Exercises actual main, its startup workers, OS signal registration and exit.
	// This creates only isolated synthetic JSON/PKI fixtures, never a native agent.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShutdownChildMain$")
	root := t.TempDir()
	cmd.Env = []string{"KVMC_SHUTDOWN_CHILD=1", "KVMC_SHUTDOWN_ROOT=" + root}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready, readDone := make(chan struct{}, 1), make(chan struct{})
	var drained atomic.Bool
	var scanErr error
	go func() {
		defer close(readDone)
		scan := bufio.NewScanner(pipe)
		for scan.Scan() {
			line := scan.Text()
			if strings.Contains(line, "listening on") {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
			if strings.Contains(line, "status=drained persistence=acknowledged backup_consistent=false") {
				drained.Store(true)
			}
		}
		scanErr = scan.Err()
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		_ = cmd.Wait()
		t.Fatal("child did not become ready")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Error(err)
	}
	// CommandContext bounds the child. Drain EOF before Wait closes StdoutPipe.
	<-readDone
	err = cmd.Wait()
	if scanErr != nil {
		t.Fatalf("child output read: %v", scanErr)
	}
	if err != nil {
		t.Fatalf("child shutdown failed: %v", err)
	}
	if !drained.Load() {
		t.Fatal("child did not report successful bounded drain")
	}
	b := NewJSONBackend(filepath.Join(root, "state.json"))
	if _, err := b.Load(context.Background()); err != nil {
		t.Fatalf("persisted fixture not reloadable: %v", err)
	}
}

type drainAgent struct {
	agentGRPCServer
	entered chan struct{}
}

func (a *drainAgent) StreamMigrationProgress(stream agentpb.AgentService_StreamMigrationProgressServer) error {
	close(a.entered)
	return a.agentGRPCServer.StreamMigrationProgress(stream)
}
func TestShutdownGRPCStreams(t *testing.T) {
	for _, multiplex := range []bool{false, true} {
		t.Run(map[bool]string{false: "dedicated", true: "multiplexed"}[multiplex], func(t *testing.T) {
			s := drainStore(&drainBackend{})
			s.hosts = map[string]*models.Host{"fixture": {ID: "fixture"}}
			s.features = models.Features{}
			c := testCoordinator(s)
			s.shutdown = c
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.cancel = cancel
			entered := make(chan struct{})
			g := grpc.NewServer(grpc.StreamInterceptor(agentAuthStreamInterceptor(s)))
			agentpb.RegisterAgentServiceServer(g, &drainAgent{agentGRPCServer: agentGRPCServer{store: s}, entered: entered})
			defer g.Stop()
			c.stopStreams = g.Stop
			var addr string
			var creds credentials.TransportCredentials = insecure.NewCredentials()
			if multiplex {
				h := httptest.NewUnstartedServer(c.handler(http.HandlerFunc(g.ServeHTTP)))
				h.EnableHTTP2 = true
				h.Config.BaseContext = func(net.Listener) context.Context { return ctx }
				h.StartTLS()
				defer h.Close()
				c.servers = []*http.Server{h.Config}
				addr = strings.TrimPrefix(h.URL, "https://")
				creds = credentials.NewTLS(&tls.Config{InsecureSkipVerify: true}) // isolated loopback fixture
			} else {
				lis, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer lis.Close()
				addr = lis.Addr().String()
				go func() { _ = g.Serve(lis) }()
			}
			conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			reqCtx, reqCancel := context.WithTimeout(authedGRPCContext(context.Background(), "fixture"), 3*time.Second)
			defer reqCancel()
			stream, err := agentpb.NewAgentServiceClient(conn).StreamMigrationProgress(reqCtx)
			if err != nil {
				t.Fatal(err)
			}
			awaitTest(t, entered)
			budget, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if err := c.drain(budget); err != nil {
				t.Fatal(err)
			}
			if _, err := stream.CloseAndRecv(); err == nil {
				t.Fatal("cancelled stream reported successful completion")
			}
		})
	}
}
func TestShutdownHijackedConsole(t *testing.T) {
	entered, peerClosed := make(chan struct{}), make(chan struct{})
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := serverWSUpgrade(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		close(entered)
		var one [1]byte
		_, _ = conn.Read(one[:])
		close(peerClosed)
	}))
	defer agent.Close()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(agent.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	s := drainStore(&drainBackend{})
	s.hosts = map[string]*models.Host{"fixture": {ID: "fixture", IP: host, AgentPort: port}}
	s.features = models.Features{}
	s.consoleSecret = "synthetic-console-fixture"
	c := testCoordinator(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.cancel = cancel
	srv := httptest.NewUnstartedServer(c.handler(handleConsoleWSProxy(s)))
	srv.Config.BaseContext = func(net.Listener) context.Context { return ctx }
	srv.Start()
	defer srv.Close()
	c.servers = []*http.Server{srv.Config}
	client, _, err := dialWSClient(strings.TrimPrefix(srv.URL, "http://"), "/api/console-ws/fixture/vm", "binary")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	awaitTest(t, entered)
	budget, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := c.drain(budget); err != nil {
		t.Fatal(err)
	}
	awaitTest(t, peerClosed)
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := client.Read(one[:]); err != io.EOF {
		t.Fatalf("browser socket not closed: %v", err)
	}
}
func TestShutdownListenerFailure(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Addr: lis.Addr().String()}
	defer lis.Close()
	failures := make(chan error, 1)
	go func() { failures <- srv.ListenAndServe() }()
	c := testCoordinator(drainStore(&drainBackend{}))
	c.servers = []*http.Server{srv}
	if err := c.wait(nil, failures, time.Second); err == nil {
		t.Fatal("listener failure reported successful exit")
	}
}
