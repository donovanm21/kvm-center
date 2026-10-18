package main

import (
	"encoding/json"
	"io"
	"kvm-center/internal/growprotocol"
	"net/http"
	"strings"
	"time"
)

const guestGrowCollection = "guest_grow_state"

type growSession struct {
	ID   string
	Seen time.Time
}

func validateGuestGrows(s *growprotocol.State) error {
	if s.Validate() != nil {
		return growprotocol.ErrRefused
	}
	if s != nil {
		for _, r := range s.Records {
			if r.Phase == "reserved" {
				return growprotocol.ErrRefused
			}
		}
	}
	return nil
}
func (s *Store) growUniqueLocked(id string) bool {
	count := 0
	for _, vs := range s.vms {
		for _, v := range vs {
			if v != nil && strings.EqualFold(v.UUID, id) {
				count++
			}
		}
	}
	return count == 1
}
func (s *Store) GrowStatus(id string) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.growUniqueLocked(id) {
		return nil, growprotocol.ErrRefused
	}
	rows := []map[string]any{}
	for _, r := range s.guestGrows.Clone().Records {
		if r.Request.Target.VMUUID == id {
			phase := r.Phase
			if phase == "queued" && !s.guestGrowOwners[r.Request.ID] {
				phase = "recovery_fenced"
			}
			if s.guestGrowUncertain {
				phase = "persistence_uncertain"
			}
			rows = append(rows, map[string]any{"request": r.Request, "status": phase, "outcome": r.Outcome})
		}
	}
	session := ""
	cap := s.growSessions[id]
	if time.Since(cap.Seen) < 5*time.Minute {
		session = cap.ID
	}
	return map[string]any{"version": 1, "guest_session": session, "records": rows, "native_verified": false}, nil
}
func (s *Store) QueueGrow(r growprotocol.Request) error {
	s.guestGrowMu.Lock()
	defer s.guestGrowMu.Unlock()
	if r.Validate() != nil {
		return growprotocol.ErrRefused
	}
	s.mu.Lock()
	next := s.guestGrows.Clone()
	for _, old := range next.Records {
		if old.Request.ID == r.ID {
			same := old.Request == r
			s.mu.Unlock()
			if same {
				return nil
			}
			return growprotocol.ErrRefused
		}
		if growprotocol.Overlap(old.Request.Target, r.Target) {
			s.mu.Unlock()
			return growprotocol.ErrRefused
		}
	}
	cap := s.growSessions[r.Target.VMUUID]
	if s.guestGrowUncertain || !s.growUniqueLocked(r.Target.VMUUID) || cap.ID != r.Session || time.Since(cap.Seen) >= 5*time.Minute {
		s.mu.Unlock()
		return growprotocol.ErrRefused
	}
	next.Records = append(next.Records, growprotocol.Record{Request: r, Phase: "queued"})
	if next.Validate() != nil {
		s.mu.Unlock()
		return growprotocol.ErrRefused
	}
	s.guestGrows = next
	s.guestGrowUncertain = true
	s.mu.Unlock()
	if s.SaveChecked() != nil {
		return growprotocol.ErrRefused
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guestGrowUncertain = false
	if s.guestGrowOwners == nil {
		s.guestGrowOwners = map[string]bool{}
	}
	s.guestGrowOwners[r.ID] = true
	return nil
}

// Outcomes correlate to immutable intent, including previous guest sessions.
// Retransmission requires the same live guest process session and durable offer.
func (s *Store) ProcessGrowReport(id string, p growprotocol.Report) (*growprotocol.Request, error) {
	s.guestGrowMu.Lock()
	defer s.guestGrowMu.Unlock()
	if p.Validate() != nil {
		return nil, growprotocol.ErrRefused
	}
	s.mu.Lock()
	if !s.growUniqueLocked(id) || s.guestGrowUncertain {
		s.mu.Unlock()
		return nil, growprotocol.ErrRefused
	}
	next := s.guestGrows.Clone()
	changed := false
	for _, o := range p.Outcomes {
		found := false
		for i, r := range next.Records {
			if r.Request.ID == o.ID {
				found = true
				if r.Request.Target.VMUUID != id || !o.Matches(r.Request) || (r.Phase != "offered" && r.Phase != "reported") || (r.Outcome != nil && *r.Outcome != o) {
					s.mu.Unlock()
					return nil, growprotocol.ErrRefused
				}
				if r.Outcome == nil {
					cp := o
					next.Records[i].Phase = "reported"
					next.Records[i].Outcome = &cp
					changed = true
				}
			}
		}
		if !found {
			s.mu.Unlock()
			return nil, growprotocol.ErrRefused
		}
	}
	var offer *growprotocol.Request
	for i, r := range next.Records {
		if r.Request.Target.VMUUID == id && r.Request.Session == p.Session && (r.Phase == "offered" || r.Phase == "queued" && s.guestGrowOwners[r.Request.ID]) {
			cp := r.Request
			offer = &cp
			if r.Phase == "queued" {
				next.Records[i].Phase = "offered"
				changed = true
			}
			break
		}
	}
	if changed {
		s.guestGrows = next
		s.guestGrowUncertain = true
	}
	s.mu.Unlock()
	if changed && s.SaveChecked() != nil {
		return nil, growprotocol.ErrRefused
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if changed {
		s.guestGrowUncertain = false
	}
	if s.growSessions == nil {
		s.growSessions = map[string]growSession{}
	}
	s.growSessions[id] = growSession{p.Session, time.Now()}
	return offer, nil
}
func handleGuestGrow(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			view, e := s.GrowStatus(r.URL.Query().Get("uuid"))
			if e != nil {
				writeError(w, 409, e.Error())
				return
			}
			writeJSON(w, 200, view)
		case http.MethodPost:
			raw, e := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
			var req growprotocol.Request
			if e != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &req) != nil {
				writeError(w, 400, "invalid grow request")
				return
			}
			if e = s.QueueGrow(req); e != nil {
				writeError(w, 409, e.Error())
				return
			}
			view, _ := s.GrowStatus(req.Target.VMUUID)
			writeJSON(w, 202, view)
		default:
			writeError(w, 405, "GET or POST only")
		}
	}
}
