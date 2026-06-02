package main

import (
	"sync"
	"testing"
	"time"
)

// TestSSEHub_EvictsStalledSubscriber: a subscriber that doesn't drain
// its channel is evicted after sseMaxConsecutiveDrops consecutive
// Publish calls.
func TestSSEHub_EvictsStalledSubscriber(t *testing.T) {
	hub := NewSSEHub()
	sub := hub.subscribe()
	if sub == nil {
		t.Fatal("subscribe returned nil")
	}
	// Fill the channel buffer without reading — next Publish calls
	// start incrementing the drop counter.
	for i := 0; i < sseBufferSize; i++ {
		hub.Publish(EventInventory, nil)
	}
	// Publish sseMaxConsecutiveDrops more times; on the final one
	// the subscriber should be evicted.
	for i := 0; i < sseMaxConsecutiveDrops; i++ {
		hub.Publish(EventInventory, nil)
	}
	// Give the hub a beat to finish the eviction (it's synchronous
	// but paranoia is cheap in a test).
	time.Sleep(10 * time.Millisecond)
	hub.mu.RLock()
	_, stillIn := hub.subs[sub]
	hub.mu.RUnlock()
	if stillIn {
		t.Fatal("stalled subscriber was NOT evicted")
	}
}

// TestSSEHub_SuccessResetsDropCounter: a subscriber that drops once
// then catches up should not be evicted.
func TestSSEHub_SuccessResetsDropCounter(t *testing.T) {
	hub := NewSSEHub()
	sub := hub.subscribe()
	if sub == nil {
		t.Fatal("subscribe returned nil")
	}
	// Fill buffer
	for i := 0; i < sseBufferSize; i++ {
		hub.Publish(EventInventory, nil)
	}
	// One drop
	hub.Publish(EventInventory, nil)
	if sub.drops.Load() != 1 {
		t.Fatalf("expected drop counter 1, got %d", sub.drops.Load())
	}
	// Drain one event
	<-sub.ch
	// Next publish succeeds and resets counter
	hub.Publish(EventInventory, nil)
	if sub.drops.Load() != 0 {
		t.Fatalf("drop counter not reset after successful publish, got %d", sub.drops.Load())
	}
}

// TestSSEHub_CapRejects: subscribe returns nil once the cap is hit.
func TestSSEHub_CapRejects(t *testing.T) {
	hub := NewSSEHub()
	var subs []*sseSub
	for i := 0; i < sseMaxSubscribers; i++ {
		s := hub.subscribe()
		if s == nil {
			t.Fatalf("hit nil at i=%d, expected cap=%d", i, sseMaxSubscribers)
		}
		subs = append(subs, s)
	}
	if got := hub.subscribe(); got != nil {
		t.Fatal("subscribe past the cap returned non-nil")
	}
	// Clean up so later tests don't see the stale subs map.
	var wg sync.WaitGroup
	for _, s := range subs {
		wg.Add(1)
		go func(s *sseSub) {
			defer wg.Done()
			hub.unsubscribe(s)
		}(s)
	}
	wg.Wait()
}
