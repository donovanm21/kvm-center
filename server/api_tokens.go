package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// ListAPITokens returns every token in the store (admin view).
func (s *Store) ListAPITokens() []*models.APIToken {
	return s.listTokensFiltered(func(*models.APIToken) bool { return true })
}

// ListAPITokensForUser returns only tokens owned by the given user —
// used by the self-service /api/tokens/me endpoint.
func (s *Store) ListAPITokensForUser(userID string) []*models.APIToken {
	return s.listTokensFiltered(func(t *models.APIToken) bool { return t.OwnerID == userID })
}

func (s *Store) listTokensFiltered(keep func(*models.APIToken) bool) []*models.APIToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.APIToken, 0, len(s.apiTokens))
	for _, t := range s.apiTokens {
		if !keep(t) {
			continue
		}
		cp := *t
		cp.Hash = "" // never leak the hash
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// GetAPIToken returns a single token by ID (for ownership checks).
func (s *Store) GetAPIToken(id string) *models.APIToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t, ok := s.apiTokens[id]; ok {
		cp := *t
		cp.Hash = ""
		return &cp
	}
	return nil
}

// validateScopes accepts a possibly-nil slice of permission names and
// returns a clean, deduplicated, sorted slice — or an error if any
// entry isn't in models.AllPermissions. Returns nil for nil/empty
// input so the legacy "inherit owner perms" path stays distinguishable
// from "explicitly empty scopes" (which would be a bug).
func validateScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, nil
	}
	allowed := map[string]bool{}
	for _, p := range models.AllPermissions {
		allowed[p] = true
	}
	seen := map[string]bool{}
	cleaned := make([]string, 0, len(scopes))
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !allowed[s] {
			return nil, fmt.Errorf("unknown permission scope %q (must be one of: %s)", s, strings.Join(models.AllPermissions, ", "))
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		cleaned = append(cleaned, s)
	}
	sort.Strings(cleaned)
	return cleaned, nil
}

// CreateAPIToken mints a new token, stores the hash, returns the
// plaintext to the caller exactly once. `scopes` (v5.51+) optionally
// narrows the token's permission set below the owner's; nil/empty
// keeps legacy "inherit all owner perms" behaviour.
func (s *Store) CreateAPIToken(name, ownerID, kind string, expiresAt time.Time, scopes []string) (full string, t *models.APIToken, err error) {
	if name == "" {
		return "", nil, errors.New("name required")
	}
	if ownerID == "" {
		return "", nil, errors.New("owner required")
	}
	if kind != "user" && kind != "system" {
		return "", nil, errors.New("kind must be \"user\" or \"system\"")
	}
	// Validate scopes against the canonical permission list. Unknown
	// strings are rejected rather than silently accepted — a typo at
	// mint time would otherwise produce a token that's too permissive
	// (the unknown string just never matches a perm check).
	cleanedScopes, err := validateScopes(scopes)
	if err != nil {
		return "", nil, err
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
		Kind:      kind,
		Hash:      hex.EncodeToString(sum[:]),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: expiresAt,
		Scopes:    cleanedScopes,
	}
	s.mu.Lock()
	if s.apiTokens == nil {
		s.apiTokens = map[string]*models.APIToken{}
	}
	s.apiTokens[id] = t
	s.mu.Unlock()
	s.AsyncSave()
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
	s.AsyncSave()
	return nil
}

// LookupAPIToken validates the bearer string, returns the owner user,
// and stamps last-used. nil + nil means "not a kvmc_ bearer" (caller
// falls back to session auth).
// LookupAPITokenWithID is the audited variant — also returns the
// token ID so middleware can attach it to the request context for
// audit attribution. Kept for back-compat callers; new code that
// needs the token's scopes should call LookupAPITokenWithRecord.
func (s *Store) LookupAPITokenWithID(bearer string) (*models.User, string) {
	u, t := s.LookupAPITokenWithRecord(bearer)
	if t == nil {
		return u, ""
	}
	return u, t.ID
}

// LookupAPITokenWithRecord (v5.51+) returns both the resolved owner
// user AND the underlying token record. Middleware uses the record's
// Scopes to narrow what subsequent permission checks accept. Returns
// (nil, nil) on miss; (user, nil) is impossible — token was found
// but the record vanished, which would indicate a torn read in the
// store.
func (s *Store) LookupAPITokenWithRecord(bearer string) (*models.User, *models.APIToken) {
	u := s.LookupAPIToken(bearer)
	if u == nil {
		return nil, nil
	}
	rest := strings.TrimPrefix(bearer, tokenPrefix)
	dot := strings.IndexByte(rest, '.')
	if dot <= 0 {
		return u, nil
	}
	id := rest[:dot]
	s.mu.RLock()
	t := s.apiTokens[id]
	s.mu.RUnlock()
	return u, t
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

type tokenCreateReq struct {
	Name      string `json:"name"`
	ExpiresAt string `json:"expires_at,omitempty"` // RFC3339
	// Scopes (v5.51+) optional list of permission strings limiting what
	// this token can do. Empty / omitted = legacy "inherit owner
	// permissions" behaviour. Each scope must be a known permission
	// from models.AllPermissions; mint rejects unknown strings rather
	// than letting them silently no-op.
	Scopes []string `json:"scopes,omitempty"`
}

func parseExpiry(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errors.New("expires_at must be RFC3339")
	}
	return t, nil
}

// handleAPITokens — admin-scoped endpoint for SYSTEM tokens.
// GET returns every system token in the store (admin view);
// POST mints a new system token owned by the caller.
func handleAPITokens(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Include both kinds so the admin can see all tokens in the fleet.
			writeJSON(w, http.StatusOK, store.ListAPITokens())
		case http.MethodPost:
			var req tokenCreateReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
				return
			}
			u := userFromRequest(r)
			if u == nil {
				writeError(w, http.StatusUnauthorized, "not authenticated")
				return
			}
			exp, err := parseExpiry(req.ExpiresAt)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			full, t, err := store.CreateAPIToken(req.Name, u.ID, "system", exp, req.Scopes)
			if err != nil {
				audit(store, r, AuditTokenCreate, "token/"+req.Name, map[string]string{"kind": "system"}, false, err.Error())
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			detail := map[string]string{"name": t.Name, "kind": "system"}
			if len(t.Scopes) > 0 {
				detail["scopes"] = strings.Join(t.Scopes, ",")
			}
			audit(store, r, AuditTokenCreate, "token/"+t.ID, detail, true, "")
			recordOperatorTask(store, r, AuditTokenCreate, "token/"+t.ID, detail, true, "")
			cp := *t
			cp.Hash = ""
			writeJSON(w, http.StatusOK, map[string]any{"token": full, "record": cp})
		default:
			writeError(w, http.StatusMethodNotAllowed, "GET / POST only")
		}
	}
}

// handleAPIToken — DELETE /api/tokens/<id> (admin; can revoke any token).
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
		recordOperatorTask(store, r, AuditTokenRevoke, "token/"+id, nil, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// handleMyAPITokens — self-service endpoint. Any signed-in user can
// list/create their own USER tokens (default 30-day expiry when
// expires_at is blank).
func handleMyAPITokens(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFromRequest(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, store.ListAPITokensForUser(u.ID))
		case http.MethodPost:
			var req tokenCreateReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
				return
			}
			exp, err := parseExpiry(req.ExpiresAt)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			// Default: 30 days from now when operator didn't pick a date.
			// Prevents forever-tokens from accumulating on user accounts.
			if exp.IsZero() {
				exp = time.Now().Add(30 * 24 * time.Hour).UTC()
			}
			full, t, err := store.CreateAPIToken(req.Name, u.ID, "user", exp, req.Scopes)
			if err != nil {
				audit(store, r, AuditTokenCreate, "token/"+req.Name, map[string]string{"kind": "user"}, false, err.Error())
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			detail := map[string]string{"name": t.Name, "kind": "user"}
			if len(t.Scopes) > 0 {
				detail["scopes"] = strings.Join(t.Scopes, ",")
			}
			audit(store, r, AuditTokenCreate, "token/"+t.ID, detail, true, "")
			recordOperatorTask(store, r, AuditTokenCreate, "token/"+t.ID, detail, true, "")
			cp := *t
			cp.Hash = ""
			writeJSON(w, http.StatusOK, map[string]any{"token": full, "record": cp})
		default:
			writeError(w, http.StatusMethodNotAllowed, "GET / POST only")
		}
	}
}

// handleMyAPIToken — DELETE /api/tokens/me/<id>. Users can only revoke
// their own tokens; any attempt against another user's token 404s.
func handleMyAPIToken(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFromRequest(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/tokens/me/")
		if id == "" {
			writeError(w, http.StatusBadRequest, "token id required")
			return
		}
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "DELETE only")
			return
		}
		existing := store.GetAPIToken(id)
		if existing == nil || existing.OwnerID != u.ID {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
		if err := store.RevokeAPIToken(id); err != nil {
			audit(store, r, AuditTokenRevoke, "token/"+id, nil, false, err.Error())
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		audit(store, r, AuditTokenRevoke, "token/"+id, map[string]string{"kind": "user"}, true, "")
		recordOperatorTask(store, r, AuditTokenRevoke, "token/"+id, map[string]string{"kind": "user"}, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
