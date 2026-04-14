package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// API tokens (v5.5). Long-lived bearer credentials for scripted use
// of kvm-center. The token authenticates as the owner user — same
// permissions, no scoping. Wire format:
//
//   kvmc_<id>.<secret>
//      <id>     8 hex chars (public — looks the row up)
//      <secret> 48 hex chars (24 random bytes, never persisted)
//
// We hash the secret with SHA-256 (no salt — secret has 192 bits of
// entropy, brute force is irrelevant). Comparison is constant-time.

const (
	tokenPrefix    = "kvmc_"
	tokenIDChars   = 8
	tokenSecretLen = 24 // bytes
)

const (
	AuditTokenCreate = "token.create"
	AuditTokenRevoke = "token.revoke"
)

// ── Store helpers ──────────────────────────────────────────────────

func (s *Store) ListAPITokens() []*models.APIToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.APIToken, 0, len(s.apiTokens))
	for _, t := range s.apiTokens {
		cp := *t
		cp.Hash = "" // never leak the hash
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// CreateAPIToken mints a new token, stores the hash, returns the
// plaintext to the caller exactly once.
func (s *Store) CreateAPIToken(name, ownerID string, expiresAt time.Time) (full string, t *models.APIToken, err error) {
	if name == "" {
		return "", nil, errors.New("name required")
	}
	if ownerID == "" {
		return "", nil, errors.New("owner required")
	}
	id, err := randHex(tokenIDChars / 2)
	if err != nil {
		return "", nil, err
	}
	var secretBytes [tokenSecretLen]byte
	if _, err := rand.Read(secretBytes[:]); err != nil {
		return "", nil, err
	}
	secret := hex.EncodeToString(secretBytes[:])
	full = tokenPrefix + id + "." + secret
	sum := sha256.Sum256([]byte(secret))
	t = &models.APIToken{
		ID:        id,
		Name:      name,
		OwnerID:   ownerID,
		Hash:      hex.EncodeToString(sum[:]),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: expiresAt,
	}
	s.mu.Lock()
	if s.apiTokens == nil {
		s.apiTokens = map[string]*models.APIToken{}
	}
	s.apiTokens[id] = t
	s.mu.Unlock()
	go s.Save()
	return full, t, nil
}

func (s *Store) RevokeAPIToken(id string) error {
	s.mu.Lock()
	t, ok := s.apiTokens[id]
	if !ok {
		s.mu.Unlock()
		return errors.New("token not found")
	}
	t.Revoked = true
	s.mu.Unlock()
	go s.Save()
	return nil
}

// LookupAPIToken validates the bearer string, returns the owner user,
// and stamps last-used. nil + nil means "not a kvmc_ bearer" (caller
// falls back to session auth).
// LookupAPITokenWithID is the audited variant — also returns the
// token ID so middleware can attach it to the request context for
// audit attribution.
func (s *Store) LookupAPITokenWithID(bearer string) (*models.User, string) {
	u := s.LookupAPIToken(bearer)
	if u == nil {
		return nil, ""
	}
	rest := strings.TrimPrefix(bearer, tokenPrefix)
	dot := strings.IndexByte(rest, '.')
	if dot <= 0 {
		return u, ""
	}
	return u, rest[:dot]
}

func (s *Store) LookupAPIToken(bearer string) *models.User {
	if !strings.HasPrefix(bearer, tokenPrefix) {
		return nil
	}
	rest := strings.TrimPrefix(bearer, tokenPrefix)
	dot := strings.IndexByte(rest, '.')
	if dot <= 0 {
		return nil
	}
	id, secret := rest[:dot], rest[dot+1:]
	s.mu.RLock()
	t, ok := s.apiTokens[id]
	s.mu.RUnlock()
	if !ok || t.Revoked {
		return nil
	}
	if !t.ExpiresAt.IsZero() && time.Now().After(t.ExpiresAt) {
		return nil
	}
	sum := sha256.Sum256([]byte(secret))
	want, _ := hex.DecodeString(t.Hash)
	if subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return nil
	}
	u := s.GetUserByID(t.OwnerID)
	if u == nil || !u.Enabled {
		return nil
	}
	// Stamp last-used outside the request hot path. Not async-safe in
	// the strict sense, but worst case we drop one tick.
	s.mu.Lock()
	t.LastUsedAt = time.Now().UTC()
	s.mu.Unlock()
	return u
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ── HTTP handlers ──────────────────────────────────────────────────

// handleAPITokens — GET list, POST create.
func handleAPITokens(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, store.ListAPITokens())
		case http.MethodPost:
			var req struct {
				Name      string `json:"name"`
				ExpiresAt string `json:"expires_at,omitempty"` // RFC3339, optional
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
				return
			}
			u := userFromRequest(r)
			if u == nil {
				writeError(w, http.StatusUnauthorized, "not authenticated")
				return
			}
			var exp time.Time
			if req.ExpiresAt != "" {
				t, err := time.Parse(time.RFC3339, req.ExpiresAt)
				if err != nil {
					writeError(w, http.StatusBadRequest, "expires_at must be RFC3339")
					return
				}
				exp = t
			}
			full, t, err := store.CreateAPIToken(req.Name, u.ID, exp)
			if err != nil {
				audit(store, r, AuditTokenCreate, "token/"+req.Name, nil, false, err.Error())
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			audit(store, r, AuditTokenCreate, "token/"+t.ID, map[string]string{"name": t.Name}, true, "")
			cp := *t
			cp.Hash = ""
			writeJSON(w, http.StatusOK, map[string]any{
				"token":  full, // shown to the operator exactly once
				"record": cp,
			})
		default:
			writeError(w, http.StatusMethodNotAllowed, "GET / POST only")
		}
	}
}

// handleAPIToken — DELETE /api/tokens/<id> revokes one token.
func handleAPIToken(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/tokens/")
		if id == "" {
			writeError(w, http.StatusBadRequest, "token id required")
			return
		}
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "DELETE only")
			return
		}
		if err := store.RevokeAPIToken(id); err != nil {
			audit(store, r, AuditTokenRevoke, "token/"+id, nil, false, err.Error())
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		audit(store, r, AuditTokenRevoke, "token/"+id, nil, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
