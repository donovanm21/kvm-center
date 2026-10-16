package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func TestTaskStatsSubmissionWindowAndVisibility(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 30, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)
	s := newTestStore(t)
	actor := &models.User{ID: "fixture-operator"}
	oldDone := since.Add(-time.Hour)
	recentDone := now.Add(-time.Minute)
	entries := []struct {
		id, actor, outcome string
		at                 time.Time
		done               *time.Time
	}{
		{"old-complete", actor.ID, "failed", since.Add(-2 * time.Hour), &oldDone},
		{"old-recent-complete", actor.ID, "success", since.Add(-time.Hour), &recentDone},
		{"old-pending", actor.ID, "pending", since.Add(-time.Hour), nil},
		{"before-boundary", actor.ID, "failed", since.Add(-time.Nanosecond), &recentDone},
		{"boundary", actor.ID, "failed", since, &recentDone},
		{"recent", actor.ID, "success", now.Add(-2 * time.Minute), &recentDone},
		{"pending", actor.ID, "pending", now.Add(-time.Minute), nil},
		{"upper-boundary", actor.ID, "pending", now, nil},
		{"future", actor.ID, "pending", now.Add(time.Minute), nil},
		{"other-user", "other", "failed", now.Add(-time.Minute), &recentDone},
	}
	for _, e := range entries {
		s.commandLog.Record(&models.CommandLogEntry{RequestID: e.id, ActorID: e.actor, Action: "START", Outcome: e.outcome, SubmittedAt: e.at, AppliedAt: e.done})
	}
	for _, query := range []string{"", "?since=" + strconv.FormatInt(since.Unix(), 10)} {
		t.Run(query, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/tasks/stats"+query, nil)
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, actor))
			w := httptest.NewRecorder()
			handleTasksStatsWithClock(s, func() time.Time { return now })(w, r)
			var got struct {
				Since     int64
				Now       int64
				Running   int
				ByOutcome map[string]int `json:"by_outcome"`
				ByAction  []actionStat   `json:"by_action"`
				Hourly    map[int64]int
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || got.Since != since.Unix() || got.Now != now.Unix() {
				t.Fatalf("window: %s", w.Body)
			}
			if got.Running != 2 || !reflect.DeepEqual(got.ByOutcome, map[string]int{"failed": 1, "success": 1, "pending": 1}) {
				t.Fatalf("counts: %s", w.Body)
			}
			wantHourly := map[int64]int{since.Truncate(time.Hour).Unix(): 1, now.Truncate(time.Hour).Unix(): 2}
			if !reflect.DeepEqual(got.Hourly, wantHourly) {
				t.Fatalf("hourly: %s", w.Body)
			}
			wantAvg := int((recentDone.Sub(since).Milliseconds() + 60000) / 2)
			if len(got.ByAction) != 1 || got.ByAction[0].Count != 3 || got.ByAction[0].AvgMs != wantAvg {
				t.Fatalf("actions: %s", w.Body)
			}
		})
	}
}
