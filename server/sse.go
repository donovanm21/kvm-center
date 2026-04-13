package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// Server-Sent Events fan-out.
//
// The hub holds one subscriber channel per connected browser and lets
// any part of the server publish a typed event. Subscribers receive
// JSON payloads over a long-lived HTTP response; the React client maps
// event types to React Query keys and invalidates its cache on each
// notification, so the UI reflects fleet state within a few hundred ms
// of the agent reporting instead of waiting on the next 15 s poll.

// Event types. Keep the set small and stable — the client hard-codes
// the mapping from type to query keys.
const (
	EventInventory = "inventory" // host / VM / storage set updated by agent telemetry
	EventMigration = "migration" // migration record started / progressed / finished
	EventSettings  = "settings"  // global feature flags changed
	EventUsers     = "users"     // user CRUD (admin console changes)
	EventGroups    = "groups"
	EventRoles     = "roles"
	EventIDPs      = "idps"
)

type sseEvent struct {
	Type    string
	Payload map[string]any // optional; use nil when "just invalidate"
}

type SSEHub struct {
	mu   sync.RWMutex
	subs map[chan sseEvent]struct{}
}

func NewSSEHub() *SSEHub {
	return &SSEHub{subs: make(map[chan sseEvent]struct{})}
}

func (h *SSEHub) subscribe() chan sseEvent {
	ch := make(chan sseEvent, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *SSEHub) unsubscribe(ch chan sseEvent) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// Publish fans an event out to every current subscriber. A slow
// subscriber whose channel is full is silently dropped for this event —
// the next event will either be received or the client will fall back
// to its 60 s safety-net refetch.
func (h *SSEHub) Publish(typ string, payload map[string]any) {
	if h == nil {
		return
	}
	ev := sseEvent{Type: typ, Payload: payload}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			// subscriber backlog full — skip rather than block
		}
	}
}

// handleSSEEvents is the long-lived /api/events handler.
func handleSSEEvents(hub *SSEHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// Tell any reverse proxy (nginx, traefik) not to buffer the body.
		w.Header().Set("X-Accel-Buffering", "no")

		ch := hub.subscribe()
		defer hub.unsubscribe(ch)

		// Prime the connection so the browser's EventSource commits
		// immediately (rather than waiting on the first event).
		fmt.Fprintf(w, ": connected %d\n\n", time.Now().Unix())
		flusher.Flush()

		heartbeat := time.NewTicker(20 * time.Second)
		defer heartbeat.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-heartbeat.C:
				// Comment lines are ignored by EventSource but keep
				// intermediaries from timing the socket out.
				if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case ev, ok := <-ch:
				if !ok {
					return
				}
				var data []byte
				if ev.Payload != nil {
					var err error
					data, err = json.Marshal(ev.Payload)
					if err != nil {
						log.Printf("sse: marshal event %s: %v", ev.Type, err)
						continue
					}
				} else {
					data = []byte("{}")
				}
				if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}
