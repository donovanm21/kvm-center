package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
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

// Subscriber hygiene — belt + braces against a stuck reverse proxy
// or a paused tab holding the connection open while its buffer fills
// up. Tier S #5, v5.32.17.
const (
	// sseMaxSubscribers caps simultaneous SSE clients. A realistic
	// operator browser opens 1–2; this headroom is enough for the
	// biggest deploy we can imagine + any lingering half-closed
	// sockets waiting on TCP timeouts.
	sseMaxSubscribers = 256
	// sseMaxConsecutiveDrops is the number of events in a row we'll
	// tolerate skipping for a given slow subscriber before evicting
	// it. Drops mean the 32-entry channel buffer is full; three in a
	// row signals the consumer has stalled, not merely paused.
	sseMaxConsecutiveDrops = 3
	// sseBufferSize is the per-subscriber channel backlog. Sized for
	// two full rounds of fan-out across every event type plus a few
	// heartbeats, so a momentary GC pause or React render storm on a
	// browser tab doesn't immediately trip sseMaxConsecutiveDrops.
	sseBufferSize = 64
)

type sseEvent struct {
	Type    string
	Payload map[string]any // optional; use nil when "just invalidate"
}

// sseSub wraps a subscriber channel with a drop counter. Eviction
// fires once the counter hits sseMaxConsecutiveDrops, so a subscriber
// can recover as long as it's keeping up.
type sseSub struct {
	ch             chan sseEvent
	drops          atomic.Int32
	evictRequested atomic.Bool
}

type SSEHub struct {
	mu   sync.RWMutex
	subs map[*sseSub]struct{}
}

func NewSSEHub() *SSEHub {
	return &SSEHub{subs: make(map[*sseSub]struct{})}
}

// subscribe returns a new subscriber, or nil if the cap is hit. The
// caller must check nil and respond 503 to the client.
func (h *SSEHub) subscribe() *sseSub {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= sseMaxSubscribers {
		log.Printf("sse: subscriber cap reached (%d); rejecting new client", sseMaxSubscribers)
		return nil
	}
	sub := &sseSub{ch: make(chan sseEvent, sseBufferSize)}
	h.subs[sub] = struct{}{}
	return sub
}

func (h *SSEHub) unsubscribe(sub *sseSub) {
	h.mu.Lock()
	if _, ok := h.subs[sub]; ok {
		delete(h.subs, sub)
		close(sub.ch)
	}
	h.mu.Unlock()
}

// Publish fans an event out to every current subscriber. A slow
// subscriber whose channel is full is silently dropped for this
// event AND gets its drop counter bumped; after
// sseMaxConsecutiveDrops in a row the subscriber is marked for
// eviction. Successful deliveries reset the counter.
func (h *SSEHub) Publish(typ string, payload map[string]any) {
	if h == nil {
		return
	}
	ev := sseEvent{Type: typ, Payload: payload}
	var toEvict []*sseSub
	h.mu.RLock()
	for sub := range h.subs {
		select {
		case sub.ch <- ev:
			sub.drops.Store(0)
		default:
			n := sub.drops.Add(1)
			if n >= sseMaxConsecutiveDrops && !sub.evictRequested.Swap(true) {
				toEvict = append(toEvict, sub)
			}
		}
	}
	h.mu.RUnlock()
	for _, sub := range toEvict {
		log.Printf("sse: evicting stalled subscriber after %d consecutive drops", sseMaxConsecutiveDrops)
		h.unsubscribe(sub)
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
		sub := hub.subscribe()
		if sub == nil {
			http.Error(w, "event stream at capacity, retry shortly", http.StatusServiceUnavailable)
			return
		}
		defer hub.unsubscribe(sub)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// Tell any reverse proxy (nginx, traefik) not to buffer the body.
		w.Header().Set("X-Accel-Buffering", "no")

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
			case ev, ok := <-sub.ch:
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
