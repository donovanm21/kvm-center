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
	"reflect"
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
	Version          int                    `json:"version"`
	SourceContract   string                 `json:"source_contract"`
	Originals        map[string][]byte      `json:"originals"`
	Reservation      *Reservation           `json:"reservation,omitempty"`
	ReservationPhase string                 `json:"reservation_phase,omitempty"`
	Aborted          map[string]Reservation `json:"aborted,omitempty"`
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
	if (s.Version != 1 && s.Version != 2) || s.SourceContract != Source || len(s.Originals) == 0 || len(s.Originals) > 128 {
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
	if s.Version == 1 && (s.ReservationPhase != "" || len(s.Aborted) != 0) {
		return nil, errors.New("v1 affinity cannot claim reconciliation evidence")
	}
	if s.Version == 2 {
		if len(s.Aborted) > 256 {
			return nil, errors.New("affinity aborted ledger full")
		}
		if s.Reservation == nil && s.ReservationPhase != "" {
			return nil, errors.New("phase without reservation")
		}
		if s.Reservation != nil && s.ReservationPhase != "prepared" && s.ReservationPhase != "dispatch_possible" {
			return nil, errors.New("invalid reservation phase")
		}
		for id, q := range s.Aborted {
			if id != q.RequestID || !validReservation(q) {
				return nil, errors.New("invalid aborted reservation")
			}
			if s.Reservation != nil && id == s.Reservation.RequestID {
				return nil, errors.New("aborted request cannot be reserved again")
			}
		}
	}
	// A state admitted for persistence must always fit the strict decoder.
	encoded, err := json.Marshal(wire(*s))
	if err != nil {
		return nil, err
	}
	if len(encoded) > 2*MaxBytes {
		return nil, errors.New("encoded affinity state too large")
	}
	return rules, nil
}
func (s *State) Validate() error { _, err := s.Rules(); return err }
func (s *State) Clone() *State {
	if s == nil {
		return nil
	}
	n := *s
	n.Aborted = nil
	if s.Aborted != nil {
		n.Aborted = map[string]Reservation{}
		for k, v := range s.Aborted {
			n.Aborted[k] = v
		}
	}
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
		if k != "version" && k != "source_contract" && k != "originals" && k != "reservation" && k != "reservation_phase" && k != "aborted" {
			return errors.New("unsupported affinity JSON field or alias")
		}
	}
	checkReservation := func(r json.RawMessage) error {
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
		return nil
	}
	if r, ok := fields["reservation"]; ok {
		if err := checkReservation(r); err != nil {
			return err
		}
	}
	if raw, ok := fields["reservation_phase"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("null reservation phase")
	}
	if raw, ok := fields["aborted"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("null aborted ledger")
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return err
		}
		for _, r := range entries {
			if err := checkReservation(r); err != nil {
				return err
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
	if n.Version == 1 {
		for _, key := range []string{"reservation_phase", "aborted"} {
			if _, ok := fields[key]; ok {
				return errors.New("v1 state cannot carry v2 fields")
			}
		}
	}
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
	if _, ok := s.Aborted[q.RequestID]; ok {
		return nil, errors.New("aborted affinity request cannot replay; use a new request identity")
	}
	if len(s.Aborted) >= 256 {
		return nil, errors.New("affinity aborted ledger full; no records may be discarded")
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

func validReservation(q Reservation) bool {
	return textOK(q.RequestID) && uuid.MatchString(q.UUID) && textOK(q.Name) && textOK(q.Source) && textOK(q.Destination) && q.Source != q.Destination && net.ParseIP(q.DestinationAddress) != nil
}

// ValidateTransition prevents ordinary persistence from discarding originals,
// aborted identities, or any reservation that may have dispatched. A prepared
// abort is authorized only by the process-local owner in the server.
func ValidateTransition(old, next *State) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if old == nil {
		return nil
	}
	if next == nil || !reflect.DeepEqual(old.Originals, next.Originals) {
		return errors.New("immutable affinity originals changed")
	}
	if reflect.DeepEqual(old, next) {
		return nil
	}
	if old.Version == 1 && old.Reservation != nil {
		return errors.New("legacy reservation has unknown dispatch phase")
	}
	if next.Version != 2 {
		return errors.New("affinity ledger downgrade refused")
	}
	expected := old.Clone()
	expected.Version = 2
	if old.Reservation == nil {
		if next.Reservation == nil || next.ReservationPhase != "prepared" {
			return errors.New("new reservation must be prepared")
		}
		if _, ok := old.Aborted[next.Reservation.RequestID]; ok {
			return errors.New("aborted request replay")
		}
		expected.Reservation = next.Reservation
		expected.ReservationPhase = "prepared"
	} else if old.ReservationPhase == "prepared" {
		if next.Reservation != nil {
			expected.ReservationPhase = "dispatch_possible"
		} else {
			if expected.Aborted == nil {
				expected.Aborted = map[string]Reservation{}
			}
			expected.Aborted[old.Reservation.RequestID] = *old.Reservation
			expected.Reservation = nil
			expected.ReservationPhase = ""
		}
	} else {
		return errors.New("dispatch-possible reservation cannot be cleared or replaced")
	}
	if !reflect.DeepEqual(expected, next) {
		return errors.New("invalid affinity ledger transition")
	}
	return nil
}
