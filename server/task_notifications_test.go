package main

import (
	"testing"

	"kvm-center/internal/models"
)

// captureFireStore swaps out the package-global fire path with a
// capture so we can assert what would have been dispatched without
// pulling in the full notification subsystem.
type captureFire struct {
	events []*models.NotificationEvent
}

func TestNotifyTaskOutcome_FailureFiresWhenFlagOn(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	if !s.GetFeatures().NotifyOnTaskFailure {
		t.Fatal("fresh store should default NotifyOnTaskFailure on")
	}
	// Subscribe to the dispatcher's input by swapping the channel.
	captured := make(chan *models.NotificationEvent, 4)
	prev := s.notifyQueue
	s.notifyQueue = captured
	t.Cleanup(func() { s.notifyQueue = prev })

	notifyTaskOutcome(s, &models.CommandLogEntry{
		RequestID: "req_x",
		Action:    "DELETE_SNAPSHOT",
		VMName:    "web01",
	}, "failed", "watchdog timeout")

	select {
	case ev := <-captured:
		if ev.Type != "command.failed" || ev.Severity != "critical" {
			t.Fatalf("event = %+v", ev)
		}
	default:
		t.Fatal("expected a fired event for failed outcome")
	}
}

func TestNotifyTaskOutcome_FailureSilentWhenFlagOff(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	feats := s.GetFeatures()
	feats.NotifyOnTaskFailure = false
	s.SetFeatures(feats)

	captured := make(chan *models.NotificationEvent, 1)
	prev := s.notifyQueue
	s.notifyQueue = captured
	t.Cleanup(func() { s.notifyQueue = prev })

	notifyTaskOutcome(s, &models.CommandLogEntry{RequestID: "req_y", Action: "STOP"}, "failed", "boom")
	select {
	case ev := <-captured:
		t.Fatalf("flag off should suppress, got %+v", ev)
	default:
	}
}

func TestNotifyTaskOutcome_SuccessNoOpUntilOptIn(t *testing.T) {
	s := NewStore(t.TempDir() + "/data.json")
	t.Cleanup(func() { s.saveWG.Wait() })
	u := &models.User{ID: "u1", Username: "alice", Enabled: true}
	if err := s.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	captured := make(chan *models.NotificationEvent, 1)
	prev := s.notifyQueue
	s.notifyQueue = captured
	t.Cleanup(func() { s.notifyQueue = prev })

	notifyTaskOutcome(s, &models.CommandLogEntry{ActorID: "u1", Action: "MIGRATE"}, "success", "ok")
	select {
	case ev := <-captured:
		t.Fatalf("success without opt-in should be silent, got %+v", ev)
	default:
	}

	// Opt-in for MIGRATE; should fire.
	if err := s.UpdateUser("u1", func(u *models.User) error {
		u.NotifyTaskSuccessActions = []string{"MIGRATE"}
		return nil
	}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	notifyTaskOutcome(s, &models.CommandLogEntry{ActorID: "u1", Action: "MIGRATE"}, "success", "ok")
	select {
	case ev := <-captured:
		if ev.Type != "command.success" {
			t.Fatalf("type = %s", ev.Type)
		}
	default:
		t.Fatal("opt-in success should fire")
	}
}
