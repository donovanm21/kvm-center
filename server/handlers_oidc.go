package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// ── Public: list enabled providers for the login page ─────────────

type publicProvider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func handleListProviders(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := []publicProvider{}
		for _, p := range store.ListIDPs() {
			if !p.Enabled {
				continue
			}
			// LDAP is transparent (handled inside the username/password form)
			// and doesn't produce a login button.
			if p.Kind == "ldap" {
				continue
			}
			out = append(out, publicProvider{ID: p.ID, Name: p.Name, Kind: p.Kind})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// ── OIDC start: 302 to provider ───────────────────────────────────

func handleOIDCStart(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid := r.URL.Query().Get("provider")
		p := store.GetIDP(pid)
		if p == nil || !p.Enabled {
			http.Error(w, "unknown or disabled provider", http.StatusNotFound)
			return
		}
		authURL, _, _, err := resolveEndpoints(p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		state, err := newOIDCState()
		if err != nil {
			http.Error(w, "state gen failed", http.StatusInternalServerError)
			return
		}
		// Cookie value encodes "<state>|<providerID>" so the callback can
		// verify both pieces without any server-side state store.
		http.SetCookie(w, &http.Cookie{
			Name:     oidcStateCookie,
			Value:    state + "|" + p.ID,
			Path:     "/api/auth/oidc/",
			MaxAge:   int(oidcStateTTL.Seconds()),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode, // must survive cross-site redirect back
		})
		redirectURI := absoluteURL(r, "/api/auth/oidc/callback")
		scopes := p.Scopes
		if scopes == "" {
			scopes = defaultScopesForKind(p.Kind)
		}
		q := url.Values{}
		q.Set("response_type", "code")
		q.Set("client_id", p.ClientID)
		q.Set("redirect_uri", redirectURI)
		q.Set("scope", scopes)
		q.Set("state", state)
		sep := "?"
		if strings.Contains(authURL, "?") {
			sep = "&"
		}
		http.Redirect(w, r, authURL+sep+q.Encode(), http.StatusFound)
	}
}

// ── OIDC callback: exchange + find-or-create + session + redirect ─

func handleOIDCCallback(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		if code == "" || state == "" {
			oidcFail(w, r, "missing code or state")
			return
		}
		c, err := r.Cookie(oidcStateCookie)
		if err != nil {
			oidcFail(w, r, "state cookie missing or expired")
			return
		}
		parts := strings.SplitN(c.Value, "|", 2)
		if len(parts) != 2 || parts[0] != state {
			oidcFail(w, r, "state mismatch")
			return
		}
		p := store.GetIDP(parts[1])
		if p == nil || !p.Enabled {
			oidcFail(w, r, "provider no longer enabled")
			return
		}
		// One-shot cookie.
		http.SetCookie(w, &http.Cookie{
			Name: oidcStateCookie, Value: "", Path: "/api/auth/oidc/", MaxAge: -1,
		})
		_, tokenURL, userinfoURL, err := resolveEndpoints(p)
		if err != nil {
			oidcFail(w, r, err.Error())
			return
		}
		redirectURI := absoluteURL(r, "/api/auth/oidc/callback")
		tok, err := exchangeCode(p, tokenURL, code, redirectURI)
		if err != nil {
			oidcFail(w, r, err.Error())
			return
		}
		claims, err := fetchUserinfo(userinfoURL, tok)
		if err != nil {
			oidcFail(w, r, err.Error())
			return
		}
		u, err := federatedLogin(store, p, claims)
		if err != nil {
			oidcFail(w, r, err.Error())
			return
		}
		// Federated logins skip the local TOTP check on purpose — the IdP
		// is the authoritative factor source. If you require MFA, enforce
		// it at the IdP.
		sess, err := store.CreateSession(u.ID, r.UserAgent())
		if err != nil {
			oidcFail(w, r, "could not create session")
			return
		}
		setSessionCookie(w, sess)
		go store.Save()
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// federatedLogin finds or creates the local account linked to the claim
// set returned by the IdP, applies the provider's default role on first
// provisioning, and returns the (live) user.
func federatedLogin(store *Store, p *models.IdentityProvider, claims map[string]any) (*models.User, error) {
	subjectClaim := p.SubjectClaim
	if subjectClaim == "" {
		subjectClaim = "sub"
		if p.Kind == "github" {
			subjectClaim = "id"
		}
	}
	subject := claimString(claims, subjectClaim)
	if subject == "" {
		return nil, errors.New("IdP response missing subject claim: " + subjectClaim)
	}
	emailClaim := p.EmailClaim
	if emailClaim == "" {
		emailClaim = "email"
	}
	email := claimString(claims, emailClaim)
	usernameClaim := p.UsernameClaim
	var username string
	if usernameClaim != "" {
		username = claimString(claims, usernameClaim)
	} else {
		for _, try := range []string{"preferred_username", "login", "email", "name"} {
			if v := claimString(claims, try); v != "" {
				username = v
				break
			}
		}
	}
	if username == "" {
		username = p.Kind + "_" + subject
	}

	// 1. Existing federated link wins.
	if u := store.FindUserByExternalID(p.ID, subject); u != nil {
		if !u.Enabled {
			return nil, errors.New("account is disabled")
		}
		return u, nil
	}

	// 2. Match by email to an existing local account (admins can pre-provision).
	if email != "" {
		for _, u := range store.ListUsers() {
			if strings.EqualFold(u.Email, email) && u.Enabled {
				if err := store.LinkExternalIdentity(u.ID, models.ExternalIdentity{
					ProviderID: p.ID, Subject: subject, Email: email,
				}); err != nil {
					return nil, err
				}
				return store.GetUserByID(u.ID), nil
			}
		}
	}

	// 3. Provision a new user. No password hash → local password login is
	//    disabled; the account is reachable only through this IdP until an
	//    admin sets one.
	username = uniqueUsername(store, username)
	u := &models.User{
		Username:  username,
		Email:     email,
		Enabled:   true,
		CreatedAt: time.Now(),
	}
	if p.DefaultRoleID != "" {
		u.RoleIDs = []string{p.DefaultRoleID}
	}
	u.ExternalIDs = []models.ExternalIdentity{{
		ProviderID: p.ID, Subject: subject, Email: email, LinkedAt: time.Now(),
	}}
	if err := store.CreateUser(u); err != nil {
		return nil, err
	}
	return u, nil
}

func uniqueUsername(store *Store, base string) string {
	base = strings.ToLower(strings.TrimSpace(base))
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, base)
	if base == "" {
		base = "user"
	}
	cand := base
	for i := 2; store.GetUserByUsername(cand) != nil; i++ {
		cand = base + "_" + itoa(i)
	}
	return cand
}

// ── Admin CRUD: /api/idps ─────────────────────────────────────────

func handleIDPsList(store *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ps := store.ListIDPs()
		out := make([]*models.IdentityProvider, 0, len(ps))
		for _, p := range ps {
			out = append(out, sanitizeIDP(p))
		}
		writeJSON(w, http.StatusOK, out)
	})
}

func handleIDPsCreate(store *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var p models.IdentityProvider
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := store.CreateIDP(&p); err != nil {
			audit(store, r, AuditIDPCreate, "idp/"+p.Name, map[string]string{"kind": p.Kind}, false, err.Error())
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		go store.Save()
		audit(store, r, AuditIDPCreate, "idp/"+p.ID, map[string]string{"name": p.Name, "kind": p.Kind}, true, "")
		writeJSON(w, http.StatusOK, sanitizeIDP(&p))
	})
}

func handleIDPUpdate(store *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/idps/")
		if id == "" {
			writeError(w, http.StatusBadRequest, "provider id required")
			return
		}
		switch r.Method {
		case http.MethodDelete:
			if err := store.DeleteIDP(id); err != nil {
				audit(store, r, AuditIDPDelete, "idp/"+id, nil, false, err.Error())
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			go store.Save()
			audit(store, r, AuditIDPDelete, "idp/"+id, nil, true, "")
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			var req struct {
				Name          *string            `json:"name"`
				Kind          *string            `json:"kind"`
				Enabled       *bool              `json:"enabled"`
				Issuer        *string            `json:"issuer"`
				AuthURL       *string            `json:"auth_url"`
				TokenURL      *string            `json:"token_url"`
				UserinfoURL   *string            `json:"userinfo_url"`
				ClientID      *string            `json:"client_id"`
				ClientSecret  *string            `json:"client_secret"`
				Scopes        *string            `json:"scopes"`
				DefaultRoleID *string            `json:"default_role_id"`
				SubjectClaim  *string            `json:"subject_claim"`
				EmailClaim    *string            `json:"email_claim"`
				UsernameClaim *string            `json:"username_claim"`
				LDAP          *models.LDAPConfig `json:"ldap"`
				SAML          *models.SAMLConfig `json:"saml"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			err := store.UpdateIDP(id, func(p *models.IdentityProvider) error {
				if req.Name != nil {
					p.Name = *req.Name
				}
				if req.Kind != nil {
					p.Kind = *req.Kind
				}
				if req.Enabled != nil {
					p.Enabled = *req.Enabled
				}
				if req.Issuer != nil {
					p.Issuer = *req.Issuer
				}
				if req.AuthURL != nil {
					p.AuthURL = *req.AuthURL
				}
				if req.TokenURL != nil {
					p.TokenURL = *req.TokenURL
				}
				if req.UserinfoURL != nil {
					p.UserinfoURL = *req.UserinfoURL
				}
				if req.ClientID != nil {
					p.ClientID = *req.ClientID
				}
				// Empty secret means "keep current" — prevents the UI from
				// wiping it, since we never send it back down.
				if req.ClientSecret != nil && *req.ClientSecret != "" {
					p.ClientSecret = *req.ClientSecret
				}
				if req.Scopes != nil {
					p.Scopes = *req.Scopes
				}
				if req.DefaultRoleID != nil {
					p.DefaultRoleID = *req.DefaultRoleID
				}
				if req.SubjectClaim != nil {
					p.SubjectClaim = *req.SubjectClaim
				}
				if req.EmailClaim != nil {
					p.EmailClaim = *req.EmailClaim
				}
				if req.UsernameClaim != nil {
					p.UsernameClaim = *req.UsernameClaim
				}
				if req.SAML != nil {
					p.SAML = req.SAML
				}
				if req.LDAP != nil {
					// Empty bind_password means "keep current" for LDAP, same
					// behaviour as client_secret for OAuth2.
					if p.LDAP == nil {
						p.LDAP = &models.LDAPConfig{}
					}
					prevPw := p.LDAP.BindPassword
					*p.LDAP = *req.LDAP
					if req.LDAP.BindPassword == "" {
						p.LDAP.BindPassword = prevPw
					}
				}
				return nil
			})
			if err != nil {
				audit(store, r, AuditIDPUpdate, "idp/"+id, nil, false, err.Error())
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			go store.Save()
			audit(store, r, AuditIDPUpdate, "idp/"+id, nil, true, "")
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	})
}

// ── Helpers ───────────────────────────────────────────────────────

func sanitizeIDP(p *models.IdentityProvider) *models.IdentityProvider {
	if p == nil {
		return nil
	}
	cp := *p
	cp.ClientSecret = "" // never emit on the wire
	if cp.LDAP != nil {
		l := *cp.LDAP
		l.BindPassword = ""
		cp.LDAP = &l
	}
	return &cp
}

func defaultScopesForKind(kind string) string {
	switch kind {
	case "github":
		return "read:user user:email"
	}
	return "openid email profile"
}

func absoluteURL(r *http.Request, path string) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if xf := r.Header.Get("X-Forwarded-Host"); xf != "" {
		host = xf
	}
	return scheme + "://" + host + path
}

// oidcFail sends the browser back to the login page with an ?oidc_error=
// query string so the UI can surface what went wrong.
func oidcFail(w http.ResponseWriter, r *http.Request, msg string) {
	u := "/?oidc_error=" + url.QueryEscape(msg)
	http.Redirect(w, r, u, http.StatusFound)
}
