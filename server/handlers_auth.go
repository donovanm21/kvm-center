package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"kvm-center/internal/models"
)

const sessionCookieName = "kvm_session"

type ctxKey string

const userCtxKey ctxKey = "user"

// ── Public auth endpoints ──────────────────────────────────────────

// GET /api/auth/status — whether any users exist. Unauthenticated.
// Used by the UI to decide whether to show the registration form or the
// login form on first load.
func handleAuthStatus(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"has_users": store.UserCount() > 0,
		})
	}
}

// POST /api/auth/register — one-shot creation of the first admin account.
// After any user exists this endpoint is locked down (403).
func handleAuthRegister(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if store.UserCount() > 0 {
			writeError(w, http.StatusForbidden, "registration closed — at least one user exists")
			return
		}
		var req struct {
			Username string `json:"username"`
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := validateCredentials(req.Username, req.Password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		hash, err := hashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not hash password")
			return
		}
		u := &models.User{
			Username:     req.Username,
			Email:        req.Email,
			PasswordHash: hash,
			Enabled:      true,
			RoleIDs:      []string{"role_admin"},
		}
		if err := store.CreateUser(u); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Start a session immediately so the admin doesn't have to log in again.
		sess, err := store.CreateSession(u.ID, r.UserAgent())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not create session")
			return
		}
		setSessionCookie(w, sess)
		go store.Save()
		writeJSON(w, http.StatusOK, map[string]any{
			"user":        sanitizeUser(u),
			"permissions": permListFromMap(effectivePermissions(store, u)),
		})
	}
}

// POST /api/auth/login — { username, password } → sets session cookie.
func handleAuthLogin(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			TOTPCode string `json:"totp_code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		u := store.GetUserByUsername(req.Username)
		localOK := u != nil && u.Enabled && u.PasswordHash != "" && verifyPassword(req.Password, u.PasswordHash)
		if !localOK {
			// Fall through to any enabled LDAP/AD providers. Each is tried
			// in order; first success wins. LDAP auth skips the local
			// TOTP check because the directory is the authoritative source.
			ldapUser, ldapErr := tryLDAPLogin(store, req.Username, req.Password)
			if ldapErr == nil && ldapUser != nil {
				sess, err := store.CreateSession(ldapUser.ID, r.UserAgent())
				if err != nil {
					writeError(w, http.StatusInternalServerError, "could not create session")
					return
				}
				setSessionCookie(w, sess)
				go store.Save()
				auditAs(store, r, ldapUser.ID, ldapUser.Username, AuditLogin, "session", map[string]string{"via": "ldap"}, true, "")
				writeJSON(w, http.StatusOK, map[string]any{
					"user":        sanitizeUser(ldapUser),
					"permissions": permListFromMap(effectivePermissions(store, ldapUser)),
				})
				return
			}
			auditAs(store, r, "", req.Username, AuditLogin, "session", map[string]string{"via": "local"}, false, "invalid credentials")
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		// Second factor, if enrolled. We signal "need code" with a distinct
		// status so the UI can prompt for it without revealing whether the
		// password itself was valid to an unauthenticated caller — the
		// password check already succeeded here, so it's fine to surface.
		if u.TOTPEnabled {
			if req.TOTPCode == "" {
				writeJSON(w, http.StatusAccepted, map[string]any{"totp_required": true})
				return
			}
			if !verifyTOTP(u.TOTPSecret, req.TOTPCode) {
				auditAs(store, r, u.ID, u.Username, AuditLogin, "session", map[string]string{"via": "local"}, false, "invalid TOTP")
				writeError(w, http.StatusUnauthorized, "invalid authentication code")
				return
			}
		}
		sess, err := store.CreateSession(u.ID, r.UserAgent())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not create session")
			return
		}
		setSessionCookie(w, sess)
		go store.Save()
		auditAs(store, r, u.ID, u.Username, AuditLogin, "session", map[string]string{"via": "local"}, true, "")
		writeJSON(w, http.StatusOK, map[string]any{
			"user":        sanitizeUser(u),
			"permissions": permListFromMap(effectivePermissions(store, u)),
		})
	}
}

// POST /api/auth/logout — clear session cookie and delete server-side record.
func handleAuthLogout(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Capture the actor before we nuke the session, so the audit row
		// names the person who logged out rather than "anonymous".
		u := userFromRequest(r)
		if c, err := r.Cookie(sessionCookieName); err == nil {
			store.DeleteSession(c.Value)
		}
		if u != nil {
			auditAs(store, r, u.ID, u.Username, AuditLogout, "session", nil, true, "")
		}
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// GET /api/auth/me — current user + effective permissions.
func handleAuthMe(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFromRequest(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"user":        sanitizeUser(u),
			"permissions": permListFromMap(effectivePermissions(store, u)),
		})
	}
}

// ── TOTP 2FA enrollment ────────────────────────────────────────────
// All three endpoints require an authenticated session (attached by
// authMiddleware); they operate on the *current* user only.

// POST /api/auth/2fa/setup — generates a pending secret and returns the
// otpauth URI so the UI can show a QR or manual-entry key. Does NOT enable
// 2FA; call /enable with a verification code to do that. Calling setup
// again before enabling overwrites the pending secret.
func handleTOTPSetup(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFromRequest(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if u.TOTPEnabled {
			writeError(w, http.StatusBadRequest, "2FA already enabled — disable first to re-enroll")
			return
		}
		secret, err := newTOTPSecret()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not generate secret")
			return
		}
		if err := store.UpdateUser(u.ID, func(x *models.User) error {
			x.TOTPSecret = secret
			x.TOTPEnabled = false
			return nil
		}); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"secret":       secret,
			"otpauth_uri":  otpauthURI(u.Username, secret),
			"issuer":       totpIssuer,
			"account":      u.Username,
		})
	}
}

// POST /api/auth/2fa/enable — { code } — verifies the pending secret
// against the submitted TOTP and, on success, flips TOTPEnabled=true.
func handleTOTPEnable(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFromRequest(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		var req struct{ Code string `json:"code"` }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if u.TOTPSecret == "" {
			writeError(w, http.StatusBadRequest, "call /2fa/setup first")
			return
		}
		if !verifyTOTP(u.TOTPSecret, req.Code) {
			writeError(w, http.StatusUnauthorized, "code did not match — check your authenticator and try again")
			return
		}
		if err := store.UpdateUser(u.ID, func(x *models.User) error {
			x.TOTPEnabled = true
			return nil
		}); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		go store.Save()
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": true})
	}
}

// POST /api/auth/2fa/disable — { password } — requires the current
// password so that a stolen session cookie alone can't strip 2FA off.
func handleTOTPDisable(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFromRequest(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		var req struct{ Password string `json:"password"` }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if !verifyPassword(req.Password, u.PasswordHash) {
			writeError(w, http.StatusUnauthorized, "incorrect password")
			return
		}
		if err := store.UpdateUser(u.ID, func(x *models.User) error {
			x.TOTPEnabled = false
			x.TOTPSecret = ""
			return nil
		}); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		go store.Save()
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": false})
	}
}

// ── Users / Groups / Roles CRUD ────────────────────────────────────

func handleUsersList(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		us := store.ListUsers()
		out := make([]*models.User, 0, len(us))
		for _, u := range us {
			out = append(out, sanitizeUser(u))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleUsersCreate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			Username string   `json:"username"`
			Email    string   `json:"email"`
			Password string   `json:"password"`
			Enabled  bool     `json:"enabled"`
			RoleIDs  []string `json:"role_ids"`
			GroupIDs []string `json:"group_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := validateCredentials(req.Username, req.Password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		hash, err := hashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "hash error")
			return
		}
		u := &models.User{
			Username: req.Username, Email: req.Email, PasswordHash: hash,
			Enabled: req.Enabled, RoleIDs: req.RoleIDs, GroupIDs: req.GroupIDs,
		}
		if err := store.CreateUser(u); err != nil {
			audit(store, r, AuditUserCreate, "user/"+req.Username, nil, false, err.Error())
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		audit(store, r, AuditUserCreate, "user/"+u.ID, map[string]string{"username": u.Username}, true, "")
		writeJSON(w, http.StatusOK, sanitizeUser(u))
	}
}

func handleUserUpdate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/users/")
		if r.Method == http.MethodDelete {
			// Guard: don't allow deleting the last enabled admin.
			if isLastAdminID(store, id) {
				writeError(w, http.StatusForbidden, "cannot delete the last admin user")
				return
			}
			if err := store.DeleteUser(id); err != nil {
				audit(store, r, AuditUserDelete, "user/"+id, nil, false, err.Error())
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			audit(store, r, AuditUserDelete, "user/"+id, nil, true, "")
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		if r.Method != http.MethodPut && r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "PUT/PATCH/DELETE only")
			return
		}
		var req struct {
			Email    *string   `json:"email,omitempty"`
			Enabled  *bool     `json:"enabled,omitempty"`
			Password *string   `json:"password,omitempty"`
			RoleIDs  *[]string `json:"role_ids,omitempty"`
			GroupIDs *[]string `json:"group_ids,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		// Hoist the "last admin" guards OUT of the UpdateUser callback —
		// UpdateUser holds a write lock on store.mu, and isLastAdminID
		// calls store.ListUsers() which takes a read lock on the SAME
		// mutex. sync.RWMutex isn't re-entrant, so running the check
		// inside the callback deadlocks the request (what the user hit
		// when editing bob's roles from the UI).
		if req.Enabled != nil && !*req.Enabled && isLastAdminID(store, id) {
			writeError(w, http.StatusBadRequest, "cannot disable the last admin user")
			return
		}
		if req.RoleIDs != nil && isLastAdminID(store, id) && !containsString(*req.RoleIDs, "role_admin") {
			writeError(w, http.StatusBadRequest, "cannot remove admin role from the last admin user")
			return
		}
		err := store.UpdateUser(id, func(u *models.User) error {
			if req.Email != nil {
				u.Email = *req.Email
			}
			if req.Enabled != nil {
				u.Enabled = *req.Enabled
			}
			if req.Password != nil && *req.Password != "" {
				h, err := hashPassword(*req.Password)
				if err != nil {
					return err
				}
				u.PasswordHash = h
			}
			if req.RoleIDs != nil {
				u.RoleIDs = *req.RoleIDs
			}
			if req.GroupIDs != nil {
				u.GroupIDs = *req.GroupIDs
			}
			return nil
		})
		if err != nil {
			audit(store, r, AuditUserUpdate, "user/"+id, nil, false, err.Error())
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		detail := map[string]string{}
		if req.Enabled != nil {
			detail["enabled"] = boolToStr(*req.Enabled)
		}
		if req.RoleIDs != nil {
			detail["roles"] = strings.Join(*req.RoleIDs, ",")
		}
		if req.Password != nil && *req.Password != "" {
			detail["password"] = "changed"
		}
		audit(store, r, AuditUserUpdate, "user/"+id, detail, true, "")
		writeJSON(w, http.StatusOK, sanitizeUser(store.GetUserByID(id)))
	}
}

func handleGroupsList(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, store.ListGroups())
	}
}

func handleGroupsCreate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var g models.Group
		if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := store.CreateGroup(&g); err != nil {
			audit(store, r, AuditGroupCreate, "group/"+g.Name, nil, false, err.Error())
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		audit(store, r, AuditGroupCreate, "group/"+g.ID, map[string]string{"name": g.Name}, true, "")
		writeJSON(w, http.StatusOK, g)
	}
}

func handleGroupUpdate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/groups/")
		if r.Method == http.MethodDelete {
			if err := store.DeleteGroup(id); err != nil {
				audit(store, r, AuditGroupDelete, "group/"+id, nil, false, err.Error())
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			audit(store, r, AuditGroupDelete, "group/"+id, nil, true, "")
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		var req struct {
			Name        *string   `json:"name,omitempty"`
			Description *string   `json:"description,omitempty"`
			RoleIDs     *[]string `json:"role_ids,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		err := store.UpdateGroup(id, func(g *models.Group) error {
			if req.Name != nil {
				g.Name = *req.Name
			}
			if req.Description != nil {
				g.Description = *req.Description
			}
			if req.RoleIDs != nil {
				g.RoleIDs = *req.RoleIDs
			}
			return nil
		})
		if err != nil {
			audit(store, r, AuditGroupUpdate, "group/"+id, nil, false, err.Error())
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		audit(store, r, AuditGroupUpdate, "group/"+id, nil, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleRolesList(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"roles":       store.ListRoles(),
			"permissions": models.AllPermissions,
		})
	}
}

func handleRolesCreate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var role models.Role
		if err := json.NewDecoder(r.Body).Decode(&role); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := store.CreateRole(&role); err != nil {
			audit(store, r, AuditRoleCreate, "role/"+role.Name, nil, false, err.Error())
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		audit(store, r, AuditRoleCreate, "role/"+role.ID, map[string]string{"name": role.Name, "permissions": strings.Join(role.Permissions, ",")}, true, "")
		writeJSON(w, http.StatusOK, role)
	}
}

func handleRoleUpdate(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/roles/")
		if r.Method == http.MethodDelete {
			if err := store.DeleteRole(id); err != nil {
				audit(store, r, AuditRoleDelete, "role/"+id, nil, false, err.Error())
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			audit(store, r, AuditRoleDelete, "role/"+id, nil, true, "")
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		var req struct {
			Name        *string   `json:"name,omitempty"`
			Description *string   `json:"description,omitempty"`
			Permissions *[]string `json:"permissions,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		err := store.UpdateRole(id, func(r *models.Role) error {
			if req.Name != nil {
				r.Name = *req.Name
			}
			if req.Description != nil {
				r.Description = *req.Description
			}
			if req.Permissions != nil {
				r.Permissions = *req.Permissions
			}
			return nil
		})
		if err != nil {
			audit(store, r, AuditRoleUpdate, "role/"+id, nil, false, err.Error())
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		detail := map[string]string{}
		if req.Permissions != nil {
			detail["permissions"] = strings.Join(*req.Permissions, ",")
		}
		audit(store, r, AuditRoleUpdate, "role/"+id, detail, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ── Middleware ─────────────────────────────────────────────────────

// Paths that don't require authentication. Agents (telemetry/guest-telemetry)
// are allowed through so the fleet keeps reporting even before an admin logs in.
var publicAPIPrefixes = []string{
	"/api/auth/status",
	"/api/auth/register",
	"/api/auth/login",
	"/api/auth/logout",
	"/api/auth/providers",
	"/api/auth/oidc/",
	"/api/auth/saml/",
	"/api/telemetry",
	"/api/guest-telemetry",
	"/api/version",
}

func authMiddleware(store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Non-API requests pass through (static assets served by the embedded FS
		// handler, /health, etc.).
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		for _, p := range publicAPIPrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				next.ServeHTTP(w, r)
				return
			}
		}
		// Everything else requires a valid session.
		c, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		sess := store.TouchSession(c.Value)
		if sess == nil {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		u := store.GetUserByID(sess.UserID)
		if u == nil || !u.Enabled {
			writeError(w, http.StatusUnauthorized, "user disabled")
			return
		}
		// If TouchSession slid the expiration forward, refresh the cookie
		// so the browser keeps a matching Expires/Max-Age value.
		setSessionCookie(w, sess)
		// Attach user to request context.
		ctx := context.WithValue(r.Context(), userCtxKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requirePerm wraps a handler and returns 403 if the user lacks the permission.
func requirePerm(store *Store, perm string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := userFromRequest(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if !hasPermission(store, u, perm) {
			writeError(w, http.StatusForbidden, "missing permission: "+perm)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Helpers ────────────────────────────────────────────────────────

func setSessionCookie(w http.ResponseWriter, sess *models.Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.Token,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		MaxAge:   int(time.Until(sess.ExpiresAt).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func userFromRequest(r *http.Request) *models.User {
	u, _ := r.Context().Value(userCtxKey).(*models.User)
	return u
}

func validateCredentials(username, password string) error {
	if len(username) < 2 {
		return errorString("username must be at least 2 characters")
	}
	if len(password) < 8 {
		return errorString("password must be at least 8 characters")
	}
	return nil
}

func isLastAdminID(store *Store, id string) bool {
	users := store.ListUsers()
	admins := 0
	targetIsAdmin := false
	for _, u := range users {
		isAdmin := false
		for _, rid := range u.RoleIDs {
			if rid == "role_admin" {
				isAdmin = true
				break
			}
		}
		if isAdmin && u.Enabled {
			admins++
			if u.ID == id {
				targetIsAdmin = true
			}
		}
	}
	return targetIsAdmin && admins <= 1
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func permListFromMap(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

type errorString string

func (e errorString) Error() string { return string(e) }
