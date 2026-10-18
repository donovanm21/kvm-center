package main

import (
	"context"
	"errors"
	"fmt"
	"kvm-center/internal/models"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
)

type alarmTarget struct{ AlarmID, HostID string }
type alarmState struct {
	BreachedSince time.Time
	Firing        bool
}

const resourceAlarmInterval = 30 * time.Second

func (s *Store) GetResourceAlarms() []*models.Alarm {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := cloneAlarms(s.alarms)
	for _, a := range out {
		for key, state := range s.alarmStates {
			if key.AlarmID == a.ID && state.Firing {
				a.FiringOn = append(a.FiringOn, key.HostID)
			}
		}
		sort.Strings(a.FiringOn)
	}
	return out
}
func (s *Store) SetResourceAlarms(in []*models.Alarm) error {
	if err := validateAlarmSnapshot(in); err != nil {
		return err
	}
	next := cloneAlarms(in)
	sort.Slice(next, func(i, j int) bool { return next[i].ID < next[j].ID })
	s.mu.Lock()
	candidate := s.persistDataLocked()
	candidate.Alarms = next
	old := candidate.EntityOverlay
	if old == nil {
		old = &entityOverlay{Version: 8, Collections: map[string]map[string][]byte{}}
	}
	var err error
	if b, ok := s.backend.(*MongoBackend); ok {
		ctx, cancel := contextWithTimeout(15 * time.Second)
		_, err = b.captureEntityOverlay(ctx, &candidate)
		cancel()
	} else {
		_, err = reconcileOverlay(&candidate, old)
	}
	if err != nil {
		s.mu.Unlock()
		return err
	}
	// Retain unchanged definitions' episodes. Reset edited/disabled/deleted IDs
	// so previous thresholds or scope never leave stale firing status behind.
	previous := map[string]*models.Alarm{}
	for _, a := range s.alarms {
		previous[a.ID] = a
	}
	retained := map[string]bool{}
	for _, a := range next {
		oldDefinition := previous[a.ID]
		same := false
		if oldDefinition != nil {
			left, right := *a, *oldDefinition
			left.UpdatedAt = ""
			right.UpdatedAt = ""
			left.CreatedAt = ""
			right.CreatedAt = ""
			same = reflect.DeepEqual(left, right)
		}
		if same {
			retained[a.ID] = true
		}
	}
	for key := range s.alarmStates {
		if !retained[key.AlarmID] {
			delete(s.alarmStates, key)
		}
	}
	s.alarms = next
	s.mu.Unlock()
	s.AsyncSave()
	return nil
}
func appliesAlarmHost(a *models.Alarm, id string) bool {
	if len(a.Hosts) == 0 {
		return true
	}
	for _, candidate := range a.Hosts {
		if strings.EqualFold(candidate, id) {
			return true
		}
	}
	return false
}

// evaluateResourceAlarms uses the deployed >= trigger and <= threshold-5 clear
// boundaries, stored telemetry (including offline hosts), and per-target duration.
func (s *Store) evaluateResourceAlarms(now time.Time) []*models.NotificationEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alarmStates == nil {
		s.alarmStates = map[alarmTarget]*alarmState{}
	}
	seen := map[alarmTarget]bool{}
	events := []*models.NotificationEvent{}
	for _, a := range s.alarms {
		if !a.Enabled {
			continue
		}
		for id, h := range s.hosts {
			if !appliesAlarmHost(a, id) {
				continue
			}
			value := h.CPUUsage
			if a.Metric == alarmHostMemory {
				value = h.MemUsage
			}
			if math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			key := alarmTarget{a.ID, id}
			seen[key] = true
			state := s.alarmStates[key]
			if state == nil {
				state = &alarmState{}
				s.alarmStates[key] = state
			}
			breached := value >= a.Threshold
			if state.Firing {
				breached = value > a.Threshold-5
			}
			eventType := ""
			severity := a.Severity
			if breached {
				if state.BreachedSince.IsZero() {
					state.BreachedSince = now
				}
				if !state.Firing && now.Sub(state.BreachedSince) >= time.Duration(a.ForSeconds)*time.Second {
					state.Firing = true
					eventType = "alarm.fired"
				}
			} else {
				state.BreachedSince = time.Time{}
				if state.Firing {
					state.Firing = false
					eventType = "alarm.cleared"
					severity = "info"
				}
			}
			if eventType != "" {
				events = append(events, &models.NotificationEvent{Type: eventType, Title: a.Name, Message: fmt.Sprintf("%s: %s %.2f%% (threshold %.2f%%)", id, a.Metric, value, a.Threshold), Resource: id, Severity: severity, At: now, Detail: map[string]string{"alarm_id": a.ID, "metric": a.Metric, "target": id, "value": fmt.Sprint(value), "threshold": fmt.Sprint(a.Threshold)}})
			}
		}
	}
	for key := range s.alarmStates {
		if !seen[key] {
			delete(s.alarmStates, key)
		}
	}
	return events
}
func (s *Store) startResourceAlarmWorker(ctx context.Context) {
	s.goWork("resource-alarms", func() {
		ticker := time.NewTicker(resourceAlarmInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				for _, event := range s.evaluateResourceAlarms(now) {
					s.fireNotification(event)
				}
			}
		}
	})
}

func validateAlarmRequest(in []*models.Alarm) error {
	if in == nil {
		return errors.New("alarms must be an array")
	}
	return validateAlarmSnapshot(in)
}
