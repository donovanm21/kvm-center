package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// OAuth2 / OIDC authorization-code flow, hand-rolled on stdlib.
//
// The flow:
//   1. UI hits /api/auth/oidc/start?provider=<id>.
//   2. We set a short-lived state cookie with random nonce + the provider id
//      and 302 to the provider's authorization URL.
//   3. Provider redirects back to /api/auth/oidc/callback?code=&state=.
//   4. We verify state, exchange the code for an access token, fetch
//      userinfo, find-or-create a local user, create a session, and 302
//      the browser to "/".
//
// GitHub is not strictly OIDC but its endpoints + /user look close enough
// that we treat it as a generic OAuth2 provider with explicit endpoint
// overrides (no discovery).

const (
	oidcStateCookie = "kvm_oidc_state"
	oidcStateTTL    = 10 * time.Minute
)

var oidcHTTPClient = &http.Client{Timeout: 15 * time.Second}

// resolveEndpoints fills missing AuthURL/TokenURL/UserinfoURL from the
// issuer's .well-known/openid-configuration when available. Endpoint
// overrides on the provider always win.
func resolveEndpoints(p *models.IdentityProvider) (authURL, tokenURL, userinfoURL string, err error) {
	authURL = p.AuthURL
	tokenURL = p.TokenURL
	userinfoURL = p.UserinfoURL
	if authURL != "" && tokenURL != "" && userinfoURL != "" {
		return
	}
	if p.Issuer == "" {
		if authURL == "" || tokenURL == "" {
			err = errors.New("provider is missing auth/token URLs and no issuer to discover from")
		}
		return
	}
	disco := strings.TrimRight(p.Issuer, "/") + "/.well-known/openid-configuration"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", disco, nil)
	resp, derr := oidcHTTPClient.Do(req)
	if derr != nil {
		err = fmt.Errorf("discovery failed: %w", derr)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		err = fmt.Errorf("discovery returned %d", resp.StatusCode)
		return
	}
	var doc struct {
		AuthEndpoint     string `json:"authorization_endpoint"`
		TokenEndpoint    string `json:"token_endpoint"`
		UserinfoEndpoint string `json:"userinfo_endpoint"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return
	}
	if authURL == "" {
		authURL = doc.AuthEndpoint
	}
	if tokenURL == "" {
		tokenURL = doc.TokenEndpoint
	}
	if userinfoURL == "" {
		userinfoURL = doc.UserinfoEndpoint
	}
	return
}

// exchangeCode trades an authorization code for an access token.
func exchangeCode(p *models.IdentityProvider, tokenURL, code, redirectURI string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", p.ClientSecret)

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json") // GitHub defaults to form-encoded otherwise
	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(body))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		// Some providers (old GitHub modes) return form-encoded.
		if vals, perr := url.ParseQuery(string(body)); perr == nil {
			if at := vals.Get("access_token"); at != "" {
				return at, nil
			}
		}
		return "", fmt.Errorf("token response parse: %w", err)
	}
	if tok.AccessToken == "" {
		return "", errors.New("token endpoint returned empty access_token")
	}
	return tok.AccessToken, nil
}

// fetchUserinfo calls the userinfo endpoint with the bearer token and
// returns the raw claims map.
func fetchUserinfo(userinfoURL, accessToken string) (map[string]any, error) {
	req, _ := http.NewRequest("GET", userinfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := oidcHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("userinfo failed (%d): %s", resp.StatusCode, string(body))
	}
	var claims map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// claimString pulls a string-ish value out of the userinfo claims map.
// Numbers (GitHub's `id` is numeric) are coerced to string.
func claimString(claims map[string]any, key string) string {
	if key == "" {
		return ""
	}
	v, ok := claims[key]
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return fmt.Sprintf("%v", v)
}

// newOIDCState returns a random URL-safe nonce for the state cookie.
func newOIDCState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
