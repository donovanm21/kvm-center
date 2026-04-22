package main

// Per-request_id outcome cache — the agent's half of the idempotency
// substrate (Tier S #4, v5.32.17). The server records a command_log
// entry before dispatching; we store the agent's response here so a
// retry with the same request_id short-circuits to the cached reply
// instead of re-executing virsh. Eliminates double-applies when the
// network drops the response mid-flight.
//
// Scope: in-memory, lost on agent restart. Server's command log is
// the durable record; if the agent restarts mid-flight the boot
// reconciler marks the entry "unknown" and the operator sees it in
// the admin UI. We don't want to persist the cache to disk because
// then a stale outcome could be served across an upgrade that
// changes semantics.

import (
	"net/http"
	"sync"
	"time"
)

const (
	outcomeCacheTTL    = 10 * time.Minute
	outcomeCacheMaxLen = 1024
)

type cachedOutcome struct {
	status int
	body   []byte
	at     time.Time
}

type outcomeCache struct {
	mu      sync.Mutex
	entries map[string]cachedOutcome
}

var outcomes = &outcomeCache{entries: make(map[string]cachedOutcome)}

func (c *outcomeCache) get(id string) (cachedOutcome, bool) {
	if id == "" {
		return cachedOutcome{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok {
		return cachedOutcome{}, false
	}
	if time.Since(e.at) > outcomeCacheTTL {
		delete(c.entries, id)
		return cachedOutcome{}, false
	}
	return e, true
}

func (c *outcomeCache) put(id string, status int, body []byte) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// If we've overflowed, sweep expired entries first. If still
	// over, drop the oldest. Simple and bounded.
	if len(c.entries) >= outcomeCacheMaxLen {
		c.sweepLocked()
		if len(c.entries) >= outcomeCacheMaxLen {
			var oldestID string
			var oldestAt time.Time
			for k, v := range c.entries {
				if oldestID == "" || v.at.Before(oldestAt) {
					oldestID = k
					oldestAt = v.at
				}
			}
			delete(c.entries, oldestID)
		}
	}
	cp := make([]byte, len(body))
	copy(cp, body)
	c.entries[id] = cachedOutcome{status: status, body: cp, at: time.Now()}
}

// sweepLocked purges expired entries. Caller must hold c.mu.
func (c *outcomeCache) sweepLocked() {
	now := time.Now()
	for k, v := range c.entries {
		if now.Sub(v.at) > outcomeCacheTTL {
			delete(c.entries, k)
		}
	}
}

// recorderWriter captures the response bytes + status so the outcome
// cache can replay them verbatim on a retry.
type recorderWriter struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (rw *recorderWriter) WriteHeader(code int) {
	if rw.status == 0 {
		rw.status = code
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *recorderWriter) Write(p []byte) (int, error) {
	if rw.status == 0 {
		rw.status = http.StatusOK
	}
	rw.body = append(rw.body, p...)
	return rw.ResponseWriter.Write(p)
}
