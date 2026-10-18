package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const guestUpdateCollection = "guest_update_state"
const guestUpdateMaxCompactBytes = 1 << 20

var guestRequestID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var guestTargetID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var errGuestUpdate = errors.New("guest update request refused or recovery-fenced; retained request must not be reset")

type guestUpdateTarget struct {
	UUID  string `json:"uuid"`
	Phase string `json:"phase"`
	URL   string `json:"url"`
	OS    string `json:"os"`
	Arch  string `json:"arch"`
}
type guestUpdateRequest struct {
	ID       string              `json:"request_id"`
	Version  string              `json:"server_version"`
	Revision string              `json:"server_revision"`
	Targets  []guestUpdateTarget `json:"targets"`
}
type guestUpdateState struct {
	Version  int                  `json:"version"`
	Requests []guestUpdateRequest `json:"requests"`
}
type guestUpdateWire guestUpdateState

func (s *guestUpdateState) Validate() error {
	if s == nil {
		return nil
	}
	if s.Version != 1 || s.Requests == nil || len(s.Requests) > 256 {
		return errGuestUpdate
	}
	ids, targets := map[string]bool{}, map[string]bool{}
	for _, r := range s.Requests {
		if !guestRequestID.MatchString(r.ID) || ids[r.ID] || r.Version == "" || r.Revision == "" || len(r.Version) > 128 || len(r.Revision) > 128 || !utf8.ValidString(r.Version+r.Revision) || len(r.Targets) == 0 || len(r.Targets) > 32 {
			return errGuestUpdate
		}
		ids[r.ID] = true
		last := ""
		for _, t := range r.Targets {
			if !guestTargetID.MatchString(t.UUID) || t.UUID <= last || targets[t.UUID] {
				return errGuestUpdate
			}
			last = t.UUID
			targets[t.UUID] = true
			switch t.Phase {
			case "queued":
				if t.URL != "" || t.OS != "" || t.Arch != "" {
					return errGuestUpdate
				}
			case "offered_unconfirmed":
				u, err := url.Parse(t.URL)
				if err != nil || len(t.URL) > 2048 || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || t.Arch != "amd64" || (t.OS != "linux" && t.OS != "windows") {
					return errGuestUpdate
				}
			default:
				return errGuestUpdate
			}
		}
	}
	encoded, err := json.Marshal(guestUpdateWire(*s))
	if err != nil || len(encoded) > guestUpdateMaxCompactBytes {
		return errGuestUpdate
	}
	return nil
}
func (s *guestUpdateState) UnmarshalJSON(raw []byte) error {
	if len(raw) > 8<<20 || !utf8.Valid(raw) || uniqueJSONKeys(raw) != nil {
		return errGuestUpdate
	}
	var w guestUpdateWire
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return errGuestUpdate
	}
	// Exact fields, including empty strings/arrays, are required. Case aliases,
	// missing fields and null must never erase a retained target barrier.
	var original, canonical any
	if json.Unmarshal(raw, &original) != nil {
		return errGuestUpdate
	}
	b, _ := json.Marshal(w)
	if json.Unmarshal(b, &canonical) != nil || !reflect.DeepEqual(original, canonical) {
		return errGuestUpdate
	}
	next := guestUpdateState(w)
	if err := next.Validate(); err != nil {
		return err
	}
	*s = next
	return nil
}
func (s *guestUpdateState) clone() *guestUpdateState {
	if s == nil {
		return &guestUpdateState{Version: 1, Requests: []guestUpdateRequest{}}
	}
	out := *s
	out.Requests = append([]guestUpdateRequest{}, s.Requests...)
	for i := range out.Requests {
		out.Requests[i].Targets = append([]guestUpdateTarget{}, s.Requests[i].Targets...)
	}
	return &out
}
func guestUpdateTransition(old, next *guestUpdateState) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if old == nil {
		return nil
	}
	if next == nil || len(next.Requests) < len(old.Requests) {
		return errGuestUpdate
	}
	for i, r := range old.Requests {
		n := next.Requests[i]
		if r.ID != n.ID || r.Version != n.Version || r.Revision != n.Revision || len(r.Targets) != len(n.Targets) {
			return errGuestUpdate
		}
		for j, t := range r.Targets {
			v := n.Targets[j]
			if t.UUID != v.UUID {
				return errGuestUpdate
			}
			if t.Phase == "offered_unconfirmed" && t != v {
				return errGuestUpdate
			}
			if t.Phase == "queued" && v.Phase != "queued" && v.Phase != "offered_unconfirmed" {
				return errGuestUpdate
			}
		}
	}
	return nil
}
func (s *Store) guestUpdateQueuedLocked(id string) bool {
	if s.guestUpdates != nil {
		for _, r := range s.guestUpdates.Requests {
			for _, t := range r.Targets {
				if t.UUID == id {
					return t.Phase == "queued" && s.guestUpdateOwners[r.ID+"/"+id] && !s.guestUpdateUncertain[r.ID]
				}
			}
		}
	}
	return false
}
func (s *Store) guestUpdateViewLocked(r guestUpdateRequest) map[string]any {
	targets := make([]map[string]string, 0, len(r.Targets))
	for _, t := range r.Targets {
		phase := t.Phase
		if phase == "queued" && !s.guestUpdateOwners[r.ID+"/"+t.UUID] {
			phase = "recovery_fenced"
		}
		if s.guestUpdateUncertain[r.ID] {
			phase = "persistence_uncertain"
		}
		targets = append(targets, map[string]string{"uuid": t.UUID, "status": phase})
	}
	return map[string]any{"request_id": r.ID, "status": "retained", "count": len(targets), "targets": targets, "guest_effects_verified": false}
}
func (s *Store) QueueGuestUpdate(id string, uuids []string) (map[string]any, error) {
	s.guestUpdateMu.Lock()
	defer s.guestUpdateMu.Unlock()
	if !guestRequestID.MatchString(id) || len(uuids) == 0 || len(uuids) > 32 {
		return nil, errGuestUpdate
	}
	uuids = append([]string{}, uuids...)
	sort.Strings(uuids)
	for i, u := range uuids {
		if !guestTargetID.MatchString(u) || (i > 0 && uuids[i-1] == u) {
			return nil, errGuestUpdate
		}
	}
	s.mu.Lock()
	next := s.guestUpdates.clone()
	for _, r := range next.Requests {
		if r.ID == id {
			same := len(r.Targets) == len(uuids)
			if same {
				for i, u := range uuids {
					same = same && r.Targets[i].UUID == u
				}
			}
			view := s.guestUpdateViewLocked(r)
			s.mu.Unlock()
			if !same {
				return nil, errGuestUpdate
			}
			return view, nil
		}
	}
	for _, u := range uuids {
		count := 0
		for _, vms := range s.vms {
			for _, v := range vms {
				if v != nil && strings.EqualFold(v.UUID, u) {
					count++
				}
			}
		}
		if count != 1 {
			s.mu.Unlock()
			return nil, errGuestUpdate
		}
		for _, r := range next.Requests {
			for _, t := range r.Targets {
				if t.UUID == u {
					s.mu.Unlock()
					return nil, errGuestUpdate
				}
			}
		}
	}
	req := guestUpdateRequest{ID: id, Version: Version, Revision: GitSHA, Targets: []guestUpdateTarget{}}
	for _, u := range uuids {
		req.Targets = append(req.Targets, guestUpdateTarget{UUID: u, Phase: "queued"})
	}
	next.Requests = append(next.Requests, req)
	if err := next.Validate(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.guestUpdates = next
	if s.guestUpdateUncertain == nil {
		s.guestUpdateUncertain = map[string]bool{}
	}
	// Export is refused even during a pending save; uncertainty is cleared only
	// by this exact admission's successful durable acknowledgement.
	s.guestUpdateUncertain[id] = true
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		return nil, errGuestUpdate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.guestUpdateUncertain, id)
	if s.guestUpdateOwners == nil {
		s.guestUpdateOwners = map[string]bool{}
	}
	for _, u := range uuids {
		s.guestUpdateOwners[id+"/"+u] = true
	}
	return s.guestUpdateViewLocked(req), nil
}
func (s *Store) GuestUpdateStatus(id string) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.guestUpdates != nil {
		for _, r := range s.guestUpdates.Requests {
			if r.ID == id {
				return s.guestUpdateViewLocked(r), nil
			}
		}
	}
	return nil, errGuestUpdate
}

// OfferGuestUpdate records possible delivery BEFORE exposing a URL. Recovery or
// replay can inspect an offer but cannot issue it again. No guest ACK exists.
func (s *Store) OfferGuestUpdate(uuid, osName, arch, offerURL string) (string, string, error) {
	s.guestUpdateMu.Lock()
	defer s.guestUpdateMu.Unlock()
	s.mu.Lock()
	next := s.guestUpdates.clone()
	ri, ti := -1, -1
	for i, r := range next.Requests {
		for j, t := range r.Targets {
			if t.UUID == uuid {
				ri, ti = i, j
			}
		}
	}
	if ri < 0 {
		s.mu.Unlock()
		return "", "", nil
	}
	req := &next.Requests[ri]
	target := &req.Targets[ti]
	if target.Phase != "queued" || !s.guestUpdateOwners[req.ID+"/"+uuid] || s.guestUpdateUncertain[req.ID] {
		s.mu.Unlock()
		return req.ID, "", errGuestUpdate
	}
	// Explicit platform only; old/ambiguous reports do not consume the request.
	if (osName != "linux" && osName != "windows") || arch != "amd64" || req.Version != Version || req.Revision != GitSHA {
		s.mu.Unlock()
		return req.ID, "", errGuestUpdate
	}
	target.Phase = "offered_unconfirmed"
	target.OS = osName
	target.Arch = arch
	target.URL = offerURL
	if err := next.Validate(); err != nil {
		s.mu.Unlock()
		return req.ID, "", err
	}
	s.guestUpdates = next
	delete(s.guestUpdateOwners, req.ID+"/"+uuid)
	s.guestUpdateUncertain[req.ID] = true
	s.mu.Unlock()
	if err := s.SaveChecked(); err != nil {
		return req.ID, "", errGuestUpdate
	}
	s.mu.Lock()
	delete(s.guestUpdateUncertain, req.ID)
	s.mu.Unlock()
	return req.ID, offerURL, nil
}
