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
	"strings"
	"time"
)

func appliesAlarmVM(a *models.Alarm, v *models.VM) bool {
	if len(a.VMs) == 0 {
		return true
	}
	for _, id := range a.VMs {
		if strings.EqualFold(id, v.UUID) || strings.EqualFold(id, v.Name) {
			return true
		}
	}
	return false
}
func alarmHostOnline(h *models.Host, now time.Time) bool {
	return h.Status == models.HostOnline && now.Sub(h.LastSeen) <= 2*time.Minute
}
func finiteAlarmValue(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Name-based legacy targets must not collapse independent entities into one episode.
// Validate offline targets too: temporarily unavailable inventory may return later.
func validateAlarmInventory(d *persistData) error {
	for _, a := range d.Alarms {
		if a == nil {
			return errors.New("unsupported alarms: null definition")
		}
		if !a.Enabled {
			continue
		}
		seen := map[string]bool{}
		for id, h := range d.Hosts {
			if h == nil {
				return errors.New("unsupported alarms: null host")
			}
			switch a.Metric {
			case alarmPoolUsed:
				if !appliesAlarmHost(a, id) {
					continue
				}
				for _, p := range d.Storage[id] {
					if p == nil || p.HostID != id || strings.TrimSpace(p.Name) == "" {
						return errors.New("unsupported alarms: ambiguous pool identity")
					}
					key := id + "/" + p.Name
					if seen[key] {
						return errors.New("unsupported alarms: conflicting pool targets")
					}
					seen[key] = true
					if !finiteAlarmValue(p.CapacityGB) || !finiteAlarmValue(p.AvailableGB) || p.CapacityGB > 0 && !finiteAlarmValue((p.CapacityGB-p.AvailableGB)/p.CapacityGB*100) {
						return errors.New("unsupported alarms: nonfinite pool figures")
					}
				}
			case alarmVMStopped:
				// The deployed VM metric ignores Hosts and matches VMs by UUID or name.
				for _, v := range d.VMs[id] {
					if v == nil {
						return errors.New("unsupported alarms: null VM")
					}
					if !appliesAlarmVM(a, v) {
						continue
					}
					if v.HostID != id || strings.TrimSpace(v.Name) == "" {
						return errors.New("unsupported alarms: ambiguous VM identity")
					}
					key := strings.ToLower(v.Name)
					if seen[key] {
						return errors.New("unsupported alarms: conflicting VM name targets")
					}
					seen[key] = true
					switch strings.ToLower(string(v.State)) {
					case "", "running", "stopped", "paused", "crashed", "unknown":
					default:
						return errors.New("unsupported alarms: unknown VM state")
					}
				}
			}
		}
	}
	return nil
}

// Read only the inventory needed to disambiguate active pool/VM alarms, before
// any index creation. Raw values stay untouched; bounds match preservation admission.
func preflightAlarmInventory(ctx context.Context, db *mongo.Database, alarms []*models.Alarm) error {
	needVM, needPool := false, false
	for _, a := range alarms {
		if a.Enabled {
			needVM = needVM || a.Metric == alarmVMStopped
			needPool = needPool || a.Metric == alarmPoolUsed
		}
	}
	if !needVM && !needPool {
		return nil
	}
	d := &persistData{Alarms: alarms, Hosts: map[string]*models.Host{}, VMs: map[string][]*models.VM{}, Storage: map[string][]*models.StoragePool{}}
	cols := []string{mcoHosts}
	if needVM {
		cols = append(cols, mcoVMs)
	}
	if needPool {
		cols = append(cols, mcoStorage)
	}
	count, size := 0, 0
	for _, col := range cols {
		err := func() error {
			cur, err := db.Collection(col).Find(ctx, bson.D{}, options.Find().SetLimit(10001))
			if err != nil {
				return err
			}
			defer cur.Close(ctx)
			typ, aliases, _ := entitySchema(col)
			for cur.Next(ctx) {
				raw := cur.Current
				count++
				size += len(raw)
				if count > 10000 || len(raw) > 1<<20 || size > 16<<20 {
					return errors.New("unsupported alarms: inventory admission exceeds bounds")
				}
				if _, err := entityIdentity(raw, col); err != nil {
					return fmt.Errorf("unsupported alarms: %w", err)
				}
				value := reflect.New(typ).Interface()
				if err := bson.Unmarshal(raw, value); err != nil {
					return err
				}
				projection, err := bson.Marshal(value)
				if err != nil {
					return err
				}
				var fields bson.D
				if err := bson.Unmarshal(projection, &fields); err != nil {
					return err
				}
				fields = append(bson.D{{Key: "_id", Value: raw.Lookup("_id").StringValue()}}, fields...)
				projection, err = bson.Marshal(fields)
				if err != nil {
					return err
				}
				if _, err := preserveEntityBSON(raw, projection, typ, aliases); err != nil {
					return fmt.Errorf("unsupported alarms: %w", err)
				}
				switch v := value.(type) {
				case *models.Host:
					d.Hosts[v.ID] = v
				case *models.VM:
					d.VMs[v.HostID] = append(d.VMs[v.HostID], v)
				case *models.StoragePool:
					d.Storage[v.HostID] = append(d.Storage[v.HostID], v)
				}
			}
			return cur.Err()
		}()
		if err != nil {
			return err
		}
	}
	return validateAlarmInventory(d)
}

// Called under Store.mu before telemetry changes any inventory or side effects.
func (s *Store) preflightAlarmTelemetry(p *models.TelemetryPayload) error {
	need := false
	for _, a := range s.alarms {
		need = need || a.Enabled && (a.Metric == alarmPoolUsed || a.Metric == alarmVMStopped)
	}
	if !need {
		return nil
	}
	d := &persistData{Alarms: s.alarms, Hosts: map[string]*models.Host{}, VMs: map[string][]*models.VM{}, Storage: map[string][]*models.StoragePool{}}
	for k, v := range s.hosts {
		d.Hosts[k] = v
	}
	for k, v := range s.vms {
		d.VMs[k] = v
	}
	for k, v := range s.storage {
		d.Storage[k] = v
	}
	d.Hosts[p.HostID] = &models.Host{ID: p.HostID}
	d.VMs[p.HostID] = nil
	d.Storage[p.HostID] = nil
	for _, v := range p.VMs {
		v.HostID = p.HostID
		d.VMs[p.HostID] = append(d.VMs[p.HostID], &v)
	}
	for _, v := range p.Storage {
		v.HostID = p.HostID
		d.Storage[p.HostID] = append(d.Storage[p.HostID], &v)
	}
	return validateAlarmInventory(d)
}
