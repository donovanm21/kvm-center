package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
)

const (
	tagStateVersion  = 1
	maxTagsPerVM     = 24
	maxTaggedVMs     = 4096
	maxTagStateBytes = 1 << 20 // comfortably below Mongo's document limit
)

var tagUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var tagPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,30}[a-z0-9])?$`)

type vmTagState struct {
	Version int                 `json:"version" bson:"version"`
	ByVM    map[string][]string `json:"by_vm" bson:"by_vm"`
}
type vmTagStateWire vmTagState

func normalizeVMTags(in []string) ([]string, error) {
	if len(in) > maxTagsPerVM {
		return nil, errors.New("at most 24 tag inputs per VM")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range in {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "" {
			continue
		}
		if !tagPattern.MatchString(tag) {
			return nil, errors.New("tag must be 1–32 ASCII letters, digits or internal ._- punctuation")
		}
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out, nil
}
func (d *vmTagState) validate() error {
	if d == nil {
		return nil
	} // absent state is a legacy no-tags snapshot
	if d.Version != tagStateVersion {
		return errors.New("unsupported VM tag state version")
	}
	if d.ByVM == nil {
		return errors.New("VM tag state requires by_vm object")
	}
	if len(d.ByVM) > maxTaggedVMs {
		return errors.New("VM tag state exceeds assignment count limit")
	}
	for id, tags := range d.ByVM {
		if !tagUUIDPattern.MatchString(id) {
			return errors.New("VM tag state contains a noncanonical UUID")
		}
		normalized, err := normalizeVMTags(tags)
		if err != nil || len(tags) == 0 || !reflect.DeepEqual(tags, normalized) {
			return errors.New("VM tag state contains noncanonical tags")
		}
	}
	raw, err := json.Marshal(vmTagStateWire(*d))
	if err != nil {
		return err
	}
	if len(raw) > maxTagStateBytes {
		return errors.New("VM tag state exceeds encoded size limit")
	}
	return nil
}
func (d vmTagState) MarshalJSON() ([]byte, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(vmTagStateWire(d))
}
func (d *vmTagState) UnmarshalJSON(raw []byte) error {
	if len(raw) > 4*maxTagStateBytes {
		return errors.New("VM tag JSON exceeds input size limit")
	}
	if err := uniqueJSONKeys(raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for key := range fields {
		if key != "version" && key != "by_vm" {
			return errors.New("unknown VM tag JSON field")
		}
	}
	var decoded vmTagStateWire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("invalid VM tag state: %w", err)
	}
	next := vmTagState(decoded)
	if err := next.validate(); err != nil {
		return err
	}
	*d = next
	return nil
}

// Keep unknown and duplicate BSON fields from being silently replaced on save.
func (d *vmTagState) UnmarshalBSON(raw []byte) error {
	if len(raw) > 2*maxTagStateBytes {
		return errors.New("VM tag BSON exceeds size limit")
	}
	if err := strictBSONKeys(raw, "version", "by_vm"); err != nil {
		return err
	}
	obj := bson.Raw(raw).Lookup("by_vm")
	if obj.Type != bson.TypeEmbeddedDocument {
		return errors.New("VM tag by_vm must be an object")
	}
	if err := uniqueBSONKeys(obj.Document()); err != nil {
		return err
	}
	var decoded vmTagStateWire
	if err := bson.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	next := vmTagState(decoded)
	if err := next.validate(); err != nil {
		return err
	}
	*d = next
	return nil
}
func uniqueBSONKeys(raw bson.Raw) error {
	elements, err := raw.Elements()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range elements {
		if seen[e.Key()] {
			return errors.New("duplicate VM tag BSON field")
		}
		seen[e.Key()] = true
	}
	return nil
}
func strictBSONKeys(raw bson.Raw, allowed ...string) error {
	if err := uniqueBSONKeys(raw); err != nil {
		return err
	}
	elements, err := raw.Elements()
	if err != nil {
		return err
	}
	for _, e := range elements {
		ok := false
		for _, key := range allowed {
			if e.Key() == key {
				ok = true
			}
		}
		if !ok {
			return errors.New("unknown VM tag BSON field")
		}
	}
	return nil
}
func uniqueJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value func() error
	value = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("invalid tag field")
				}
				if seen[name] {
					return errors.New("duplicate VM tag JSON field")
				}
				seen[name] = true
				if err := value(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := value(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid VM tag JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing VM tag JSON data")
	}
	return nil
}
func cloneVMTags(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for id, tags := range in {
		out[id] = append([]string(nil), tags...)
	}
	return out
}

// SetVMTags stages a validated edit. It does not acknowledge durability: future
// callers must use SaveChecked before reporting a durable change to a user.
// Missing inventory never prunes tags; deletion requires an explicit empty edit.
func (s *Store) SetVMTags(uuid string, tags []string) error {
	id := strings.ToLower(strings.TrimSpace(uuid))
	if !tagUUIDPattern.MatchString(id) {
		return errors.New("VM tags require a canonical UUID")
	}
	normalized, err := normalizeVMTags(tags)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneVMTags(s.vmTags)
	if len(normalized) == 0 {
		delete(next, id)
	} else {
		next[id] = normalized
	}
	if err := (&vmTagState{Version: tagStateVersion, ByVM: next}).validate(); err != nil {
		return err
	}
	s.vmTags = next
	return nil
}
func (s *Store) GetVMTags(uuid string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.vmTags[strings.ToLower(strings.TrimSpace(uuid))]...)
}
func (s *Store) tagStateLocked() *vmTagState {
	if len(s.vmTags) == 0 {
		return nil
	}
	return &vmTagState{Version: tagStateVersion, ByVM: s.vmTags}
}

// Legacy snapshot fields retain their existing decoding behavior. Only the new
// namespace and the explicitly incompatible local tag namespace reject repeats.
func uniqueTagSnapshotFields(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok {
			return errors.New("invalid snapshot object")
		}
		for _, reserved := range []string{"vm_tag_state", "vm_tags"} {
			if strings.EqualFold(name, reserved) && name != reserved {
				return errors.New("noncanonical VM tag snapshot field")
			}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if name == "vm_tag_state" || name == "vm_tags" {
			if seen[name] {
				return errors.New("duplicate VM tag snapshot field")
			}
			seen[name] = true
		}
	}
	return nil
}
