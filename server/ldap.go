package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	ldap "github.com/go-ldap/ldap/v3"

	"kvm-center/internal/models"
)

// ldapAuthenticate verifies username+password against the directory and,
// on success, returns a (subject, email, username) triple so the common
// federated-provisioning path can find or create the local account.
//
// Flow:
//  1. Dial (ldap:// or ldaps://) with a 10s timeout.
//  2. If a service BindDN is set, bind with that and search for the user's
//     DN using UserFilter.
//  3. Otherwise (single-step mode) construct the DN from BindDN template
//     or fall back to "username@domain"-style binds are out of scope —
//     the filter mode covers AD and OpenLDAP equivalently.
//  4. Bind as the discovered user DN with the submitted password.
//  5. Return attributes needed by federatedLogin.
func ldapAuthenticate(p *models.IdentityProvider, username, password string) (subject, email, uname string, err error) {
	if p.LDAP == nil {
		err = errors.New("provider has no LDAP config")
		return
	}
	cfg := p.LDAP
	if password == "" {
		err = errors.New("password required")
		return
	}
	if cfg.URL == "" || cfg.UserBaseDN == "" || cfg.UserFilter == "" {
		err = errors.New("ldap provider is missing url/user_base_dn/user_filter")
		return
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}
	conn, derr := ldap.DialURL(cfg.URL, ldap.DialWithTLSConfig(tlsCfg))
	if derr != nil {
		err = fmt.Errorf("ldap dial: %w", derr)
		return
	}
	defer conn.Close()
	conn.SetTimeout(10 * time.Second)
	if cfg.StartTLS && !strings.HasPrefix(strings.ToLower(cfg.URL), "ldaps://") {
		if e := conn.StartTLS(tlsCfg); e != nil {
			err = fmt.Errorf("ldap StartTLS: %w", e)
			return
		}
	}

	// 1. Service bind (if configured) so we can search for the user's DN.
	if cfg.BindDN != "" {
		bindPw, decErr := DecryptSecret(cfg.BindPassword)
		if decErr != nil {
			err = fmt.Errorf("ldap bind_password decrypt: %w", decErr)
			return
		}
		if e := conn.Bind(cfg.BindDN, bindPw); e != nil {
			err = fmt.Errorf("ldap service bind failed: %w", e)
			return
		}
	}

	// 2. Search for the user. Filter may contain either %s (printf-style)
	//    or {username} (Mustache-ish) as the placeholder.
	filter := cfg.UserFilter
	esc := ldap.EscapeFilter(username)
	filter = strings.ReplaceAll(filter, "{username}", esc)
	if strings.Contains(filter, "%s") {
		filter = fmt.Sprintf(filter, esc)
	}
	usernameAttr := cfg.UsernameAttr
	if usernameAttr == "" {
		usernameAttr = "sAMAccountName"
	}
	emailAttr := cfg.EmailAttr
	if emailAttr == "" {
		emailAttr = "mail"
	}
	req := ldap.NewSearchRequest(
		cfg.UserBaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		1, int(10*time.Second/time.Second), false,
		filter,
		[]string{"dn", "objectGUID", "entryUUID", usernameAttr, emailAttr},
		nil,
	)
	res, serr := conn.Search(req)
	if serr != nil {
		err = fmt.Errorf("ldap search: %w", serr)
		return
	}
	if len(res.Entries) == 0 {
		err = errors.New("user not found in directory")
		return
	}
	entry := res.Entries[0]

	// 3. Bind as the user with the submitted password — this is the actual
	//    authentication. If it succeeds, the password is correct.
	if e := conn.Bind(entry.DN, password); e != nil {
		err = errors.New("invalid credentials")
		return
	}

	// 4. Assemble the (subject, email, username) tuple. Prefer a stable ID
	//    (objectGUID on AD, entryUUID on OpenLDAP); fall back to the DN so
	//    renames still resolve to the same local user.
	subject = entry.GetAttributeValue("objectGUID")
	if subject == "" {
		subject = entry.GetAttributeValue("entryUUID")
	}
	if subject == "" {
		subject = entry.DN
	}
	email = entry.GetAttributeValue(emailAttr)
	uname = entry.GetAttributeValue(usernameAttr)
	if uname == "" {
		uname = username
	}
	return
}

// tryLDAPLogin walks every enabled LDAP provider and returns the local
// user matching the first successful bind, provisioning the account if
// needed. Returns (nil, nil) when no provider matches so callers can map
// it to a generic "invalid credentials" response.
func tryLDAPLogin(store *Store, username, password string) (*models.User, error) {
	for _, p := range store.ListIDPs() {
		if !p.Enabled || p.Kind != "ldap" || p.LDAP == nil {
			continue
		}
		full := store.GetIDP(p.ID) // ListIDPs sanitizes nothing, but keep the indirection for consistency
		if full == nil {
			continue
		}
		subject, email, uname, err := ldapAuthenticate(full, username, password)
		if err != nil {
			continue // try the next provider
		}
		claims := map[string]any{
			"sub":                subject,
			"email":              email,
			"preferred_username": uname,
		}
		u, ferr := federatedLogin(store, full, claims)
		if ferr != nil {
			return nil, ferr
		}
		return u, nil
	}
	return nil, nil
}
