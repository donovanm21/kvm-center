// Package affinitypolicy implements the bounded deployed-source UUID rule contract.
// It never executes a move and never treats an execution result as placement proof.
package affinitypolicy

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"io"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const Source = "6db8c7237bd7cf507532391c0dc0c2e36a70f696"
const MaxBytes = 1 << 20

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Rule struct {
	ID, Name, Kind, Strength string
	Enabled                  bool
	VMs                      []string
}
type Reservation struct {
	RequestID          string `json:"request_id"`
	UUID               string `json:"uuid"`
	Name               string `json:"name"`
	Source             string `json:"source"`
	Destination        string `json:"destination"`
	DestinationAddress string `json:"destination_address"`
}
type State struct {
	Version        int               `json:"version"`
	SourceContract string            `json:"source_contract"`
	Originals      map[string][]byte `json:"originals"`
	Reservation    *Reservation      `json:"reservation,omitempty"`
}
type wire State

func textOK(s string) bool {
	return utf8.ValidString(s) && s != "" && len(s) <= 256 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func Decode(raw []byte) (Rule, error) {
	var r Rule
	if len(raw) < 5 || uint64(binary.LittleEndian.Uint32(raw[:4])) != uint64(len(raw)) {
		return r, errors.New("affinity BSON must contain exactly one complete document")
	}
	d := bson.Raw(raw)
	if len(raw) > MaxBytes {
		return r, errors.New("affinity original exceeds limit")
	}
	if err := d.Validate(); err != nil {
		return r, err
	}
	els, err := d.Elements()
	if err != nil {
		return r, err
	}
	seen := map[string]bool{}
	for _, e := range els {
		k := e.Key()
		if seen[k] {
			return r, errors.New("duplicate affinity BSON field")
		}
		seen[k] = true
		switch k {
		case "_id", "id", "name", "kind", "strength", "createdat", "updatedat":
			if v, ok := e.Value().StringValueOK(); !ok || !utf8.ValidString(v) {
				return r, errors.New("affinity string field has unsupported type")
			}
		case "enabled":
			if _, ok := e.Value().BooleanOK(); !ok {
				return r, errors.New("affinity enabled must be boolean")
			}
		case "vms":
		default:
			return r, errors.New("unsupported affinity field or alias")
		}
	}
	str := func(k string) string { v, _ := d.Lookup(k).StringValueOK(); return v }
	r.ID = str("_id")
	r.Name = str("name")
	r.Kind = str("kind")
	r.Strength = str("strength")
	r.Enabled, _ = d.Lookup("enabled").BooleanOK()
	if !textOK(r.ID) || str("id") != r.ID || !textOK(r.Name) || !seen["enabled"] {
		return r, errors.New("invalid or conflicting affinity identity")
	}
	if r.Kind != "together" && r.Kind != "separate" {
		return r, errors.New("unsupported affinity kind")
	}
	if r.Strength != "must" && r.Strength != "should" {
		return r, errors.New("unsupported affinity strength")
	}
	arr, ok := d.Lookup("vms").ArrayOK()
	if !ok {
		return r, errors.New("affinity members must be array")
	}
	elements, err := arr.Elements()
	if err != nil {
		return r, err
	}
	for i, e := range elements {
		if e.Key() != strconv.Itoa(i) {
			return r, errors.New("noncanonical affinity array index")
		}
	}
	vals, err := arr.Values()
	if err != nil || len(vals) < 2 || len(vals) > 64 {
		return r, errors.New("unsupported affinity member count")
	}
	members := map[string]bool{}
	for _, v := range vals {
		s, ok := v.StringValueOK()
		if !ok || !uuid.MatchString(s) || members[s] {
			return r, errors.New("affinity requires unique canonical UUID selectors; names and tags unsupported")
		}
		members[s] = true
		r.VMs = append(r.VMs, s)
	}
	return r, nil
}
func (s *State) Rules() ([]Rule, error) {
	if s == nil {
		return nil, nil
	}
	if s.Version != 1 || s.SourceContract != Source || len(s.Originals) == 0 || len(s.Originals) > 128 {
		return nil, errors.New("unsupported affinity state contract")
	}
	keys := make([]string, 0, len(s.Originals))
	total := 0
	for k, v := range s.Originals {
		keys = append(keys, k)
		total += len(v)
	}
	if total > MaxBytes {
		return nil, errors.New("affinity originals exceed limit")
	}
	sort.Strings(keys)
	rules := make([]Rule, 0, len(keys))
	for _, id := range keys {
		r, err := Decode(s.Originals[id])
		if err != nil {
			return nil, err
		}
		if r.ID != id {
			return nil, errors.New("affinity original identity mismatch")
		}
		rules = append(rules, r)
	}
	parent := map[string]string{}
	var root func(string) string
	root = func(x string) string {
		if parent[x] == "" {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = root(parent[x])
		}
		return parent[x]
	}
	for _, r := range rules {
		if r.Enabled && r.Strength == "must" && r.Kind == "together" {
			for _, v := range r.VMs[1:] {
				parent[root(v)] = root(r.VMs[0])
			}
		}
	}
	for _, r := range rules {
		if r.Enabled && r.Strength == "must" && r.Kind == "separate" {
			for i, a := range r.VMs {
				for _, b := range r.VMs[i+1:] {
					if root(a) == root(b) {
						return nil, errors.New("contradictory required affinity rules, including transitive together group")
					}
				}
			}
		}
	}
	if q := s.Reservation; q != nil {
		if !textOK(q.RequestID) || !uuid.MatchString(q.UUID) || !textOK(q.Name) || !textOK(q.Source) || !textOK(q.Destination) || q.Source == q.Destination || net.ParseIP(q.DestinationAddress) == nil {
			return nil, errors.New("invalid affinity reservation")
		}
	}
	return rules, nil
}
func (s *State) Validate() error { _, err := s.Rules(); return err }
func (s *State) Clone() *State {
	if s == nil {
		return nil
	}
	n := *s
	n.Originals = map[string][]byte{}
	for k, v := range s.Originals {
		n.Originals[k] = append([]byte(nil), v...)
	}
	if s.Reservation != nil {
		q := *s.Reservation
		n.Reservation = &q
	}
	return &n
}
func (s State) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(wire(s))
}
func (s *State) UnmarshalJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid affinity JSON UTF-8")
	}
	if len(raw) > 2*MaxBytes {
		return errors.New("affinity state too large")
	}
	if err := uniqueJSON(raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for k := range fields {
		if k != "version" && k != "source_contract" && k != "originals" && k != "reservation" {
			return errors.New("unsupported affinity JSON field or alias")
		}
	}
	if r, ok := fields["reservation"]; ok {
		if bytes.Equal(bytes.TrimSpace(r), []byte("null")) {
			return errors.New("null affinity reservation")
		}
		var q map[string]json.RawMessage
		if err := json.Unmarshal(r, &q); err != nil {
			return err
		}
		for k := range q {
			switch k {
			case "request_id", "uuid", "name", "source", "destination", "destination_address":
			default:
				return errors.New("unsupported reservation field or alias")
			}
		}
	}
	var w wire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&w); err != nil {
		return err
	}
	n := State(w)
	if err := n.Validate(); err != nil {
		return err
	}
	*s = n
	return nil
}
func uniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func() error
	value = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		v, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		seen := map[string]bool{}
		for d.More() {
			if v == '{' {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return errors.New("duplicate affinity JSON key")
				}
				seen[s] = true
			}
			if e := value(); e != nil {
				return e
			}
		}
		_, e = d.Token()
		return e
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing affinity JSON")
	}
	return nil
}

type Placement struct{ UUID, Name, Host string }

// Check uses a consistent inventory including offline hosts. Unknown members are
// refused rather than silently treated as absent. Names never select rules.
func (s *State) Check(in []Placement, q Reservation) ([]string, error) {
	rules, err := s.Rules()
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	if s.Reservation != nil {
		return nil, errors.New("unresolved affinity placement reservation; reconciliation required")
	}
	byID := map[string]Placement{}
	names := map[string]string{}
	for _, p := range in {
		if !uuid.MatchString(p.UUID) || !textOK(p.Name) || !textOK(p.Host) {
			return nil, errors.New("unsupported affinity inventory identity")
		}
		if _, ok := byID[p.UUID]; ok {
			return nil, errors.New("duplicate affinity VM identity or placement")
		}
		byID[p.UUID] = p
		key := strings.ToLower(p.Name)
		if old := names[key]; old != "" && old != p.UUID {
			return nil, errors.New("ambiguous affinity VM name")
		}
		names[key] = p.UUID
	}
	for name, id := range names {
		if p, ok := byID[name]; ok && p.UUID != id {
			return nil, errors.New("affinity UUID/name crossmatch")
		}
	}
	p, ok := byID[q.UUID]
	if !ok || p.Name != q.Name || p.Host != q.Source || q.Source == q.Destination {
		return nil, errors.New("conflicting affinity request identity or placement")
	}
	if !textOK(q.RequestID) || !textOK(q.Destination) || net.ParseIP(q.DestinationAddress) == nil {
		return nil, errors.New("invalid affinity placement request")
	}
	warnings := []string{}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		hosts := map[string]int{}
		for _, id := range r.VMs {
			p, ok := byID[id]
			if !ok {
				return nil, errors.New("unresolved affinity UUID selector")
			}
			host := p.Host
			if id == q.UUID {
				host = q.Destination
			}
			hosts[host]++
		}
		violation := r.Kind == "together" && len(hosts) > 1
		if r.Kind == "separate" {
			for _, count := range hosts {
				if count > 1 {
					violation = true
				}
			}
		}
		if violation {
			message := fmt.Sprintf("affinity rule %q (%s/%s) would be violated", r.ID, r.Kind, r.Strength)
			if r.Strength == "must" {
				return nil, errors.New(message)
			}
			warnings = append(warnings, message)
		}
	}
	return warnings, nil
}
