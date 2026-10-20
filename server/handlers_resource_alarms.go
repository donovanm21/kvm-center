package main

import (
	"encoding/json"
	"io"
	"kvm-center/internal/models"
	"net/http"
	"time"
)

func handleResourceAlarms(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, map[string]any{"alarms": s.GetResourceAlarms(), "warning": resourceAlarmDeliveryWarning(s), "evaluation_error": s.resourceAlarmEvaluationError()})
		case http.MethodPut:
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
			if err != nil || len(raw) > 1<<20 {
				writeError(w, 400, "invalid alarm request size")
				return
			}
			if err := uniqueJSONKeys(raw); err != nil {
				writeError(w, 400, "ambiguous alarm JSON")
				return
			}
			var records []json.RawMessage
			if json.Unmarshal(raw, &records) != nil || records == nil {
				writeError(w, 400, "alarms must be an array")
				return
			}
			for _, record := range records {
				var fields map[string]json.RawMessage
				if json.Unmarshal(record, &fields) != nil || fields == nil {
					writeError(w, 400, "alarm must be an object")
					return
				}
				for _, key := range []string{"name", "enabled", "metric", "threshold", "for_seconds", "severity"} {
					if _, ok := fields[key]; !ok {
						writeError(w, 400, "missing alarm definition field")
						return
					}
				}
				// Optional timestamps/scopes may be absent; nil scopes mean all
				// targets. Scalar nulls are rejected by Alarm.UnmarshalJSON.
				for key := range fields {
					switch key {
					case "id", "name", "enabled", "metric", "threshold", "for_seconds", "severity", "hosts", "vms", "created_at", "updated_at":
					default:
						writeError(w, 400, "unsupported alarm JSON fields")
						return
					}
				}
			}
			var in []*models.Alarm
			if json.Unmarshal(raw, &in) != nil {
				writeError(w, 400, "invalid alarms")
				return
			}
			existing := map[string]*models.Alarm{}
			for _, a := range s.GetResourceAlarms() {
				existing[a.ID] = a
			}
			now := time.Now().UTC().Format(time.RFC3339)
			for _, a := range in {
				if a == nil {
					writeError(w, 400, "null alarm")
					return
				}
				if a.ID == "" {
					id, err := randHex(8)
					if err != nil {
						writeError(w, 500, "cannot generate alarm identity")
						return
					}
					a.ID = "alarm-" + id
					a.CreatedAt = now
				} else if old := existing[a.ID]; old != nil {
					a.CreatedAt = old.CreatedAt
				}
				a.UpdatedAt = now
			}
			if err := validateAlarmRequest(in); err != nil {
				writeError(w, 400, err.Error())
				return
			}
			if err := s.SetResourceAlarms(in); err != nil {
				writeError(w, 400, err.Error())
				return
			}
			writeJSON(w, 200, map[string]any{"alarms": s.GetResourceAlarms(), "warning": resourceAlarmDeliveryWarning(s), "evaluation_error": s.resourceAlarmEvaluationError()})
		default:
			writeError(w, 405, "GET or PUT")
		}
	}
}
func handleResourceAlarmMetrics() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, 405, "GET only")
			return
		}
		writeJSON(w, 200, []map[string]any{
			{"key": alarmHostCPU, "label": "Hypervisor CPU usage (%)", "unit": "%", "stateful": false},
			{"key": alarmHostMemory, "label": "Hypervisor memory usage (%)", "unit": "%", "stateful": false},
			{"key": alarmHostOffline, "label": "Hypervisor stopped reporting", "unit": "seconds", "stateful": false},
			{"key": alarmPoolUsed, "label": "Storage pool used (%)", "unit": "%", "stateful": false},
			{"key": alarmVMStopped, "label": "Autostart VM is not running", "unit": "", "stateful": true},
			{"key": alarmAgentVersion, "label": "Agent version differs from control plane", "unit": "", "stateful": true},
		})
	}
}

func resourceAlarmDeliveryWarning(s *Store) string {
	cfg := s.GetNotifications()
	if cfg != nil {
		for _, ch := range cfg.Channels {
			if !ch.Enabled {
				continue
			}
			types := cfg.EventTypes
			if len(ch.EventTypes) > 0 {
				types = ch.EventTypes
			}
			if types["alarm.fired"] {
				return ""
			}
		}
	}
	return "No enabled notification channel subscribes to alarm.fired."
}
