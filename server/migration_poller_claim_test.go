package main

import "testing"

// TestClaimMigrationPoller_SingleFlight: first claim succeeds, second
// claim on the same ID is refused.
func TestClaimMigrationPoller_SingleFlight(t *testing.T) {
	id := "poller-test-" + NewRequestID()
	if !claimMigrationPoller(id) {
		t.Fatal("first claim refused")
	}
	if claimMigrationPoller(id) {
		t.Fatal("second claim on same ID was accepted — dual poller risk")
	}
	releaseMigrationPoller(id)
	if !claimMigrationPoller(id) {
		t.Fatal("re-claim after release refused")
	}
	releaseMigrationPoller(id)
}

// TestClaimMigrationPoller_DistinctIDs: independent IDs don't block
// each other.
func TestClaimMigrationPoller_DistinctIDs(t *testing.T) {
	a := "poller-a-" + NewRequestID()
	b := "poller-b-" + NewRequestID()
	if !claimMigrationPoller(a) || !claimMigrationPoller(b) {
		t.Fatal("distinct IDs blocked each other")
	}
	releaseMigrationPoller(a)
	releaseMigrationPoller(b)
}
