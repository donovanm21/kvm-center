package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
)

// JSON decoding otherwise treats struct field names case-insensitively. Walk
// typed objects, but not map identity keys, before decoding can collapse them.
func validateTypedJSONKeys(raw []byte, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	// These versioned codecs already reject unknown/ambiguous fields; time is a scalar.
	if t == reflect.TypeOf(historyState{}) || t == reflect.TypeOf(vmTagState{}) || t == reflect.TypeOf(entityOverlay{}) || t == reflect.TypeOf(time.Time{}) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
			return nil
		}
		fields := map[string]reflect.Type{}
		var collect func(reflect.Type)
		collect = func(typ reflect.Type) {
			for typ.Kind() == reflect.Pointer {
				typ = typ.Elem()
			}
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				if f.PkgPath != "" && !f.Anonymous {
					continue
				}
				tag := strings.Split(f.Tag.Get("json"), ",")[0]
				if tag == "-" {
					continue
				}
				if f.Anonymous && tag == "" {
					collect(f.Type)
					continue
				}
				if tag == "" {
					tag = f.Name
				}
				fields[tag] = f.Type
			}
		}
		collect(t)
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for key, value := range values {
			ft, ok := fields[key]
			if !ok {
				for name := range fields {
					if strings.EqualFold(key, name) {
						return errors.New("noncanonical typed JSON field")
					}
				}
				continue
			}
			if err := validateTypedJSONKeys(value, ft); err != nil {
				return err
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := validateTypedJSONKeys(value, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := validateTypedJSONKeys(value, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
