package models

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Do not let JSON null silently become a disabled alarm or zero hold duration.
func (a *Alarm) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("alarm must be an object")
	}
	scalar := map[string]bool{"id": true, "name": true, "enabled": true, "metric": true, "threshold": true, "for_seconds": true, "severity": true, "created_at": true, "updated_at": true}
	for key, value := range fields {
		if scalar[key] {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return errors.New("null alarm scalar")
			}
		} else if key != "hosts" && key != "vms" && key != "firing_on" {
			return errors.New("unknown alarm JSON field")
		}
	}
	type plain Alarm
	var next plain
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	*a = Alarm(next)
	return nil
}

func (a Alarm) MarshalJSON() ([]byte, error) {
	type plain Alarm
	return json.Marshal(struct {
		*plain
		Hosts []string `json:"hosts"`
		VMs   []string `json:"vms"`
	}{(*plain)(&a), a.Hosts, a.VMs})
}
