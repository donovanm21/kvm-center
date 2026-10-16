package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeRepeatedCleanup(t *testing.T) {
	for _, scenario := range []string{"success", "failure", "redirect", "timeout", "body-timeout", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			connections := map[net.Conn]bool{}
			var handled atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handled.Add(1)
				switch scenario {
				case "success":
					w.Write([]byte(`{"status":"ok"}`))
				case "failure":
					w.WriteHeader(503)
				case "redirect":
					w.Header().Set("Location", "https://192.0.2.1/health")
					w.WriteHeader(302)
				case "timeout":
					<-r.Context().Done()
				case "body-timeout":
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				case "oversize":
					w.Write([]byte(strings.Repeat("x", 4097)))
				}
			}))
			server.Config.ConnState = func(c net.Conn, state http.ConnState) {
				mu.Lock()
				defer mu.Unlock()
				if state == http.StateClosed {
					delete(connections, c)
				} else {
					connections[c] = true
				}
			}
			server.StartTLS()
			defer server.Close()
			for i := 0; i < 10; i++ {
				timeout := 2 * time.Second
				if scenario == "timeout" || scenario == "body-timeout" {
					timeout = 100 * time.Millisecond
				}
				err := probe(context.Background(), server.URL+"/health", timeout)
				if (err == nil) != (scenario == "success") {
					t.Fatalf("probe %d error=%v", i, err)
				}
				switch scenario {
				case "failure":
					if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
						t.Fatalf("wrong failure: %v", err)
					}
				case "redirect":
					if err == nil || !strings.Contains(err.Error(), "HTTP 302") {
						t.Fatalf("wrong redirect result: %v", err)
					}
				case "oversize":
					if err == nil || !strings.Contains(err.Error(), "exceeds 4096") {
						t.Fatalf("wrong size result: %v", err)
					}
				case "timeout", "body-timeout":
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("wrong timeout: %v", err)
					}
				}
			}
			if handled.Load() != 10 {
				t.Fatalf("fixture handled %d requests, want 10", handled.Load())
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				mu.Lock()
				remaining := len(connections)
				mu.Unlock()
				if remaining == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%d connections survived repeated probes", remaining)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
func TestProbeRejectsNonlocalAndUnboundedTargets(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1/health", "https://example.invalid/health", "https://192.0.2.1/health", "https://localhost/health", "https://user:password@127.0.0.1/health", "https://127.0.0.1/other", "https://127.0.0.1/health?x=y", "https://127.0.0.1/health#fragment"} {
		if err := probe(context.Background(), target, time.Second); err == nil {
			t.Fatalf("accepted %s", target)
		}
	}
	for _, timeout := range []time.Duration{0, -1, 5 * time.Second} {
		if err := probe(context.Background(), "https://127.0.0.1:1/health", timeout); err == nil {
			t.Fatalf("accepted timeout %v", timeout)
		}
	}
}
func TestProbeConnectionFailureAndCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	for i := 0; i < 10; i++ {
		if err := probe(context.Background(), "https://"+address+"/health", 100*time.Millisecond); err == nil {
			t.Fatal("closed port reported healthy")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := probe(ctx, "https://"+address+"/health", time.Second); err == nil {
		t.Fatal("cancelled probe succeeded")
	}
}
