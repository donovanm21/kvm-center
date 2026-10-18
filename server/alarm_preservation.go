package main

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"kvm-center/internal/models"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
)

const alarmAdmissionMax = 256
const alarmHostCPU = "host.cpu_percent"
const alarmHostMemory = "host.memory_percent"

func validateAlarm(a *models.Alarm) error {
	if a == nil || a.ID == "" || strings.TrimSpace(a.ID) != a.ID || strings.TrimSpace(a.Name) == "" {
		return errors.New("unsupported alarms: missing or ambiguous identity/name")
	}
	if a.Metric != alarmHostCPU && a.Metric != alarmHostMemory {
		return errors.New("unsupported alarms: only CPU and memory metrics admitted")
	}
	if math.IsNaN(a.Threshold) || math.IsInf(a.Threshold, 0) || a.Threshold <= 5 || a.Threshold > 100 || a.ForSeconds < 0 || a.ForSeconds > 86400 {
		return errors.New("unsupported alarms: threshold or duration")
	}
	if a.Severity != "info" && a.Severity != "warning" && a.Severity != "critical" {
		return errors.New("unsupported alarms: severity")
	}
	for _, at := range []string{a.CreatedAt, a.UpdatedAt} {
		if at != "" {
			if _, err := time.Parse(time.RFC3339, at); err != nil {
				return errors.New("unsupported alarms: timestamp")
			}
		}
	}
	for _, scope := range [][]string{a.Hosts, a.VMs} {
		for _, id := range scope {
			if id == "" || strings.TrimSpace(id) != id {
				return errors.New("unsupported alarms: ambiguous scope")
			}
		}
	}
	if len(a.FiringOn) > 0 {
		return errors.New("unsupported alarms: persisted firing state")
	}
	return nil
}
func validateAlarmSnapshot(alarms []*models.Alarm) error {
	if len(alarms) > alarmAdmissionMax {
		return errors.New("unsupported alarms: admission exceeds bound")
	}
	seen := map[string]bool{}
	for _, a := range alarms {
		if err := validateAlarm(a); err != nil {
			return err
		}
		if seen[a.ID] {
			return errors.New("unsupported alarms: duplicate identity")
		}
		seen[a.ID] = true
	}
	return nil
}
func validateAlarmBSON(raw bson.Raw) error {
	if _, err := rawFields(raw); err != nil {
		return fmt.Errorf("unsupported alarms: %w", err)
	}
	es, _ := raw.Elements()
	for _, e := range es {
		for _, alias := range []string{"for_seconds", "created_at", "updated_at", "firing_on", "firingon"} {
			if strings.EqualFold(e.Key(), alias) {
				return errors.New("unsupported alarms: BSON alias or derived state")
			}
		}
	}
	var a models.Alarm
	if err := bson.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("unsupported alarms: %w", err)
	}
	if err := validateAlarm(&a); err != nil {
		return err
	}
	id, ok := raw.Lookup("_id").StringValueOK()
	if !ok || id != a.ID {
		return errors.New("unsupported alarms: BSON identity mismatch")
	}
	candidate, err := bson.Marshal(struct {
		ID            string `bson:"_id"`
		*models.Alarm `bson:",inline"`
	}{a.ID, &a})
	if err != nil {
		return err
	}
	_, err = preserveEntityBSON(raw, candidate, reflect.TypeOf(a), nil)
	if err != nil {
		return fmt.Errorf("unsupported alarms: %w", err)
	}
	return nil
}
func loadAlarmRecords(ctx context.Context, db *mongo.Database) ([]*models.Alarm, error) {
	cur, err := db.Collection(mcoAlarms).Find(ctx, bson.D{}, options.Find().SetLimit(alarmAdmissionMax+1))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []*models.Alarm{}
	for cur.Next(ctx) {
		if len(out) == alarmAdmissionMax {
			return nil, errors.New("unsupported alarms: admission exceeds bound")
		}
		if err := validateAlarmBSON(cur.Current); err != nil {
			return nil, err
		}
		var a models.Alarm
		if err := bson.Unmarshal(cur.Current, &a); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	if err := validateAlarmSnapshot(out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func cloneAlarms(in []*models.Alarm) []*models.Alarm {
	out := make([]*models.Alarm, 0, len(in))
	for _, a := range in {
		cp := *a
		if a.Hosts != nil {
			cp.Hosts = append([]string{}, a.Hosts...)
		}
		if a.VMs != nil {
			cp.VMs = append([]string{}, a.VMs...)
		}
		cp.FiringOn = nil
		out = append(out, &cp)
	}
	return out
}
