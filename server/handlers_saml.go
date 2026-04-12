package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewjam/saml"
)

// GET /api/auth/saml/start?provider=<id>
func handleSAMLStart(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid := r.URL.Query().Get("provider")
		p := store.GetIDP(pid)
		if p == nil || !p.Enabled || p.Kind != "saml" {
			http.Error(w, "unknown or disabled provider", http.StatusNotFound)
			return
		}
		base := absoluteURL(r, "")
		sp, err := buildServiceProvider(p.SAML, base, p.ID)
		if err != nil {
			oidcFail(w, r, err.Error())
			return
		}
		req, err := sp.MakeAuthenticationRequest(sp.GetSSOBindingLocation(saml.HTTPRedirectBinding), saml.HTTPRedirectBinding, saml.HTTPPostBinding)
		if err != nil {
			oidcFail(w, r, "build authn request: "+err.Error())
			return
		}
		redirectURL, err := req.Redirect("", sp)
		if err != nil {
			oidcFail(w, r, "redirect url: "+err.Error())
			return
		}
		http.Redirect(w, r, redirectURL.String(), http.StatusFound)
	}
}

// POST /api/auth/saml/acs?provider=<id>
func handleSAMLACS(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		pid := r.URL.Query().Get("provider")
		p := store.GetIDP(pid)
		if p == nil || !p.Enabled || p.Kind != "saml" {
			oidcFail(w, r, "unknown or disabled provider")
			return
		}
		base := absoluteURL(r, "")
		sp, err := buildServiceProvider(p.SAML, base, p.ID)
		if err != nil {
			oidcFail(w, r, err.Error())
			return
		}
		if err := r.ParseForm(); err != nil {
			oidcFail(w, r, "parse form: "+err.Error())
			return
		}
		// Accept unsolicited assertions (IdP-initiated) by passing nil for
		// possibleRequestIDs; crewjam/saml still fully validates signatures
		// and temporal conditions.
		assertion, err := sp.ParseResponse(r, nil)
		if err != nil {
			// InvalidResponseError wraps a more detailed PrivateErr.
			if ire, ok := err.(*saml.InvalidResponseError); ok && ire.PrivateErr != nil {
				oidcFail(w, r, "SAML validation: "+ire.PrivateErr.Error())
				return
			}
			oidcFail(w, r, "SAML validation: "+err.Error())
			return
		}
		// Extract claims.
		claims := map[string]any{}
		if assertion.Subject != nil && assertion.Subject.NameID != nil {
			claims["sub"] = assertion.Subject.NameID.Value
		}
		for _, stmt := range assertion.AttributeStatements {
			for _, attr := range stmt.Attributes {
				if len(attr.Values) == 0 {
					continue
				}
				v := attr.Values[0].Value
				// Common SAML attribute friendly names — populate both the
				// raw name and some well-known aliases so the mapping layer
				// below works out-of-the-box with most IdPs.
				claims[attr.Name] = v
				if attr.FriendlyName != "" {
					claims[attr.FriendlyName] = v
				}
				// Azure AD / Entra emits schemas URIs; map the common ones.
				switch attr.Name {
				case "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress":
					claims["email"] = v
				case "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name",
					"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/givenname":
					if _, ok := claims["preferred_username"]; !ok {
						claims["preferred_username"] = v
					}
				case "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/nameidentifier":
					if _, ok := claims["sub"]; !ok {
						claims["sub"] = v
					}
				}
			}
		}

		// Apply per-provider attribute overrides.
		if cfg := p.SAML; cfg != nil {
			if cfg.SubjectAttr != "" {
				if v, ok := claims[cfg.SubjectAttr]; ok {
					claims["sub"] = v
				}
			}
			if cfg.EmailAttr != "" {
				if v, ok := claims[cfg.EmailAttr]; ok {
					claims["email"] = v
				}
			}
			if cfg.UsernameAttr != "" {
				if v, ok := claims[cfg.UsernameAttr]; ok {
					claims["preferred_username"] = v
				}
			}
		}
		// Fallbacks for IdPs that put email in the NameID but don't emit
		// a preferred_username attribute.
		if _, ok := claims["preferred_username"]; !ok {
			if em, _ := claims["email"].(string); em != "" {
				if at := strings.IndexByte(em, '@'); at > 0 {
					claims["preferred_username"] = em[:at]
				} else {
					claims["preferred_username"] = em
				}
			} else if sub, _ := claims["sub"].(string); sub != "" {
				claims["preferred_username"] = sub
			}
		}

		u, err := federatedLogin(store, p, claims)
		if err != nil {
			oidcFail(w, r, err.Error())
			return
		}
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

// GET /api/auth/saml/metadata?provider=<id> — serve our SP metadata so
// the IdP operator can import it in one click instead of copy-pasting
// the ACS URL and entity ID by hand.
func handleSAMLMetadata(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid := r.URL.Query().Get("provider")
		p := store.GetIDP(pid)
		if p == nil || p.Kind != "saml" {
			http.Error(w, "unknown provider", http.StatusNotFound)
			return
		}
		base := absoluteURL(r, "")
		sp, err := buildServiceProvider(p.SAML, base, p.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		md := sp.Metadata()
		// Set a 1-day cache lifetime so the IdP can periodically refresh.
		md.ValidUntil = time.Now().Add(24 * time.Hour)
		buf, err := xml.MarshalIndent(md, "", "  ")
		if err != nil {
			http.Error(w, "marshal: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/samlmetadata+xml")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="kvm-center-%s-sp-metadata.xml"`, p.ID))
		w.Write([]byte(xml.Header))
		w.Write(buf)
	}
}
