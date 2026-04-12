package main

import (
	"errors"
	"sort"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// ── IdentityProvider CRUD ──────────────────────────────────────────

func (s *Store) ListIDPs() []*models.IdentityProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.IdentityProvider, 0, len(s.idps))
	for _, p := range s.idps {
		cp := *p
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func (s *Store) GetIDP(id string) *models.IdentityProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.idps[id]
	if !ok {
		return nil
	}
	cp := *p
	return &cp
}

func (s *Store) CreateIDP(p *models.IdentityProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Name == "" || p.ClientID == "" {
		return errors.New("name and client_id required")
	}
	if p.ID == "" {
		p.ID = newID("idp")
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	s.idps[p.ID] = p
	return nil
}

func (s *Store) UpdateIDP(id string, mutate func(*models.IdentityProvider) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.idps[id]
	if !ok {
		return errors.New("provider not found")
	}
	return mutate(p)
}

func (s *Store) DeleteIDP(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.idps[id]; !ok {
		return errors.New("provider not found")
	}
	delete(s.idps, id)
	return nil
}

// FindUserByExternalID returns the local user linked to (providerID, subject),
// or nil if no account is linked yet.
func (s *Store) FindUserByExternalID(providerID, subject string) *models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		for _, ext := range u.ExternalIDs {
			if ext.ProviderID == providerID && ext.Subject == subject {
				cp := *u
				return &cp
			}
		}
	}
	return nil
}

// LinkExternalIdentity attaches an external identity to a local user. Safe
// to call repeatedly — refreshes the linked_at and email fields.
func (s *Store) LinkExternalIdentity(userID string, ext models.ExternalIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return errors.New("user not found")
	}
	ext.LinkedAt = time.Now()
	for i, e := range u.ExternalIDs {
		if e.ProviderID == ext.ProviderID && e.Subject == ext.Subject {
			u.ExternalIDs[i] = ext
			return nil
		}
	}
	u.ExternalIDs = append(u.ExternalIDs, ext)
	return nil
}
