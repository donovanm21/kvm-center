package main

import (
	"errors"
	"sort"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// sessionLifetime is the sliding idle-timeout window: a session stays valid
// for this long after its last authenticated request. Industry-standard for
// admin tools: long enough to survive a browser restart, short enough that
// an abandoned session eventually expires. Sliding means active users never
// get logged out unexpectedly.
const sessionLifetime = 7 * 24 * time.Hour

// ── Users ───────────────────────────────────────────────────────────

func (s *Store) UserCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users)
}

func (s *Store) GetUserByID(id string) *models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if u, ok := s.users[id]; ok {
		cp := *u
		return &cp
	}
	return nil
}

func (s *Store) GetUserByUsername(username string) *models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.lookupUserByUsernameLocked(username)
	if u == nil {
		return nil
	}
	cp := *u
	return &cp
}

func (s *Store) lookupUserByUsernameLocked(username string) *models.User {
	lc := strings.ToLower(username)
	for _, u := range s.users {
		if strings.ToLower(u.Username) == lc {
			return u
		}
	}
	return nil
}

func (s *Store) ListUsers() []*models.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.User, 0, len(s.users))
	for _, u := range s.users {
		cp := *u
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Username) < strings.ToLower(out[j].Username)
	})
	return out
}

func (s *Store) CreateUser(u *models.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u.Username == "" {
		return errors.New("username required")
	}
	if s.lookupUserByUsernameLocked(u.Username) != nil {
		return errors.New("username already exists")
	}
	if u.ID == "" {
		u.ID = newID("user")
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	s.users[u.ID] = u
	return nil
}

func (s *Store) UpdateUser(id string, mutate func(*models.User) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return errors.New("user not found")
	}
	return mutate(u)
}

func (s *Store) DeleteUser(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[id]; !ok {
		return errors.New("user not found")
	}
	delete(s.users, id)
	// Clean up any active sessions for this user.
	for tok, sess := range s.sessions {
		if sess.UserID == id {
			delete(s.sessions, tok)
		}
	}
	return nil
}

// ── Groups ──────────────────────────────────────────────────────────

func (s *Store) ListGroups() []*models.Group {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Group, 0, len(s.groups))
	for _, g := range s.groups {
		cp := *g
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func (s *Store) CreateGroup(g *models.Group) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g.Name == "" {
		return errors.New("name required")
	}
	for _, existing := range s.groups {
		if strings.EqualFold(existing.Name, g.Name) {
			return errors.New("group name already exists")
		}
	}
	if g.ID == "" {
		g.ID = newID("group")
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	s.groups[g.ID] = g
	return nil
}

func (s *Store) UpdateGroup(id string, mutate func(*models.Group) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		return errors.New("group not found")
	}
	return mutate(g)
}

func (s *Store) DeleteGroup(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[id]; !ok {
		return errors.New("group not found")
	}
	delete(s.groups, id)
	// Remove this group from any users' membership lists.
	for _, u := range s.users {
		filtered := u.GroupIDs[:0]
		for _, gid := range u.GroupIDs {
			if gid != id {
				filtered = append(filtered, gid)
			}
		}
		u.GroupIDs = filtered
	}
	return nil
}

// ── Roles ───────────────────────────────────────────────────────────

func (s *Store) ListRoles() []*models.Role {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Role, 0, len(s.roles))
	for _, r := range s.roles {
		cp := *r
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BuiltIn != out[j].BuiltIn {
			return out[i].BuiltIn // built-ins first
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func (s *Store) CreateRole(r *models.Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Name == "" {
		return errors.New("name required")
	}
	for _, existing := range s.roles {
		if strings.EqualFold(existing.Name, r.Name) {
			return errors.New("role name already exists")
		}
	}
	if r.ID == "" {
		r.ID = newID("role")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	r.BuiltIn = false
	s.roles[r.ID] = r
	return nil
}

func (s *Store) UpdateRole(id string, mutate func(*models.Role) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.roles[id]
	if !ok {
		return errors.New("role not found")
	}
	if r.BuiltIn {
		return errors.New("built-in roles cannot be modified")
	}
	return mutate(r)
}

func (s *Store) DeleteRole(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.roles[id]
	if !ok {
		return errors.New("role not found")
	}
	if r.BuiltIn {
		return errors.New("built-in roles cannot be deleted")
	}
	delete(s.roles, id)
	// Remove from any users / groups referencing it.
	for _, u := range s.users {
		filtered := u.RoleIDs[:0]
		for _, rid := range u.RoleIDs {
			if rid != id {
				filtered = append(filtered, rid)
			}
		}
		u.RoleIDs = filtered
	}
	for _, g := range s.groups {
		filtered := g.RoleIDs[:0]
		for _, rid := range g.RoleIDs {
			if rid != id {
				filtered = append(filtered, rid)
			}
		}
		g.RoleIDs = filtered
	}
	return nil
}

// ── Sessions ────────────────────────────────────────────────────────

func (s *Store) CreateSession(userID, userAgent string) (*models.Session, error) {
	tok, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := &models.Session{
		Token:     tok,
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: now.Add(sessionLifetime),
		UserAgent: userAgent,
	}
	s.mu.Lock()
	s.sessions[tok] = sess
	if u, ok := s.users[userID]; ok {
		u.LastLoginAt = now
	}
	s.mu.Unlock()
	return sess, nil
}

func (s *Store) GetSession(token string) *models.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[token]
	if !ok {
		return nil
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil
	}
	cp := *sess
	return &cp
}

// TouchSession slides the expiration forward when more than half the
// lifetime has been consumed. Returns the (possibly updated) session, or
// nil if it no longer exists. Safe to call on every authenticated request.
func (s *Store) TouchSession(token string) *models.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok {
		return nil
	}
	now := time.Now()
	if now.After(sess.ExpiresAt) {
		delete(s.sessions, token)
		return nil
	}
	// Only extend when less than half the window remains — avoids a write
	// on every single request.
	if sess.ExpiresAt.Sub(now) < sessionLifetime/2 {
		sess.ExpiresAt = now.Add(sessionLifetime)
	}
	cp := *sess
	return &cp
}

func (s *Store) DeleteSession(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// PurgeExpiredSessions is called periodically by a background goroutine.
func (s *Store) PurgeExpiredSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for tok, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			delete(s.sessions, tok)
		}
	}
}
