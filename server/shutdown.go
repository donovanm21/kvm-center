package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// workSet registers work before launch. Once idle, seal prevents Add/Wait races.
// It does not cancel work: its owner must stop every ingress before sealing.
type workSet struct {
	mu      sync.Mutex
	active  int
	idle    chan struct{}
	sealed  bool
	failure error
}

func (g *workSet) begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sealed {
		return false
	}
	if g.active == 0 {
		g.idle = make(chan struct{})
	}
	g.active++
	return true
}
func (g *workSet) end() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	if g.active == 0 {
		close(g.idle)
	}
}
func (g *workSet) fail(err error) { g.mu.Lock(); defer g.mu.Unlock(); g.failure = err }
func (g *workSet) seal(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		g.mu.Lock()
		if g.active == 0 {
			g.sealed = true
			err := g.failure
			g.mu.Unlock()
			return err
		}
		idle := g.idle
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idle:
		}
	}
}
func (s *Store) goWork(name string, fn func()) {
	if !s.work.begin() {
		s.work.fail(errors.New("worker submitted after drain"))
		log.Printf("shutdown: rejected worker %s", name)
		return
	}
	go func() {
		defer s.work.end()
		defer func() {
			if r := recover(); r != nil {
				s.work.fail(fmt.Errorf("worker %s panicked", name))
				log.Printf("worker %s panicked: %v", name, r)
			}
		}()
		fn()
	}()
}

// contextMutex preserves zero-value serialized persistence, with bounded wait.
type contextMutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *contextMutex) LockContext(ctx context.Context) error {
	m.once.Do(func() { m.token = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.token <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *contextMutex) Lock()   { _ = m.LockContext(context.Background()) }
func (m *contextMutex) Unlock() { <-m.token }

type shutdownCoordinator struct {
	store       *Store
	draining    atomic.Bool
	cancel      context.CancelFunc
	stopStreams func()
	servers     []*http.Server
	restart     chan struct{}
}

func (c *shutdownCoordinator) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Register before checking the gate, so no accepted request escapes drain.
		if !c.store.work.begin() {
			http.Error(w, "server draining", http.StatusServiceUnavailable)
			return
		}
		defer c.store.work.end()
		if c.draining.Load() {
			http.Error(w, "server draining", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (c *shutdownCoordinator) drain(ctx context.Context) error {
	c.draining.Store(true)
	c.cancel()
	// Close listeners/connections, but separately join handlers including hijacks.
	// Accepted handlers retain their own result semantics; cancellation is not a
	// claim that an already dispatched remote operation was cancelled.
	if c.stopStreams != nil {
		c.stopStreams()
	}
	results := make(chan error, len(c.servers))
	for _, srv := range c.servers {
		go func() { results <- srv.Shutdown(ctx) }()
	}
	for range c.servers {
		select {
		case err := <-results:
			if err != nil {
				return fmt.Errorf("HTTP drain: %w", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := c.store.work.seal(ctx); err != nil {
		return fmt.Errorf("producers: %w", err)
	}
	if err := c.store.saves.seal(ctx); err != nil {
		return fmt.Errorf("queued saves: %w", err)
	}
	if err := c.store.saveCheckedContext(ctx); err != nil {
		return fmt.Errorf("final persistence: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.store.backend.Close(ctx); err != nil {
		return fmt.Errorf("backend close: %w", err)
	}
	return ctx.Err()
}

// A single owner runs drain. A second signal aborts the budget, never starts
// another save. The outer deadline also bounds a backend/filesystem call that
// fails to honor context; main must exit on failure, not resume service.
func (c *shutdownCoordinator) wait(signals <-chan os.Signal, listenerErrors <-chan error, budget time.Duration) error {
	var cause error
	select {
	case <-signals:
	case <-c.restart:
	case cause = <-listenerErrors:
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.drain(ctx) }()
	select {
	case err := <-done:
		return errors.Join(cause, err)
	case <-signals:
		cancel()
		return errors.Join(cause, errors.New("shutdown interrupted by repeated signal; persistence outcome uncertain"))
	case <-ctx.Done():
		return errors.Join(cause, fmt.Errorf("shutdown incomplete; persistence outcome uncertain: %w", ctx.Err()))
	}
}

func serverContext() context.Context {
	if serverCtx != nil {
		return serverCtx
	}
	return context.Background()
}
