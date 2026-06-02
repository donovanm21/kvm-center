package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"

	"kvm-center/internal/models"
)

// SAML 2.0 SP-initiated sign-in.
//
// We don't sign our own AuthnRequests — many IdPs don't require it, and
// skipping SP signing keeps config ("just paste your metadata URL") down
// to two copy-paste steps. IdP assertions are still fully verified
// against the configured IdP signing certificate.

var samlHTTPClient = &http.Client{Timeout: 15 * time.Second}

// buildServiceProvider constructs a crewjam/saml ServiceProvider from
// our config + the current request's public URL.
func buildServiceProvider(cfg *models.SAMLConfig, baseURL, providerID string) (*saml.ServiceProvider, error) {
	if cfg == nil {
		return nil, errors.New("no saml config")
	}

	acsURL, err := url.Parse(baseURL + "/api/auth/saml/acs?provider=" + url.QueryEscape(providerID))
	if err != nil {
		return nil, err
	}
	metadataURL, err := url.Parse(baseURL + "/api/auth/saml/metadata?provider=" + url.QueryEscape(providerID))
	if err != nil {
		return nil, err
	}

	entityID := cfg.SPEntityID
	if entityID == "" {
		entityID = metadataURL.String()
	}

	sp := &saml.ServiceProvider{
		EntityID:          entityID,
		AcsURL:            *acsURL,
		MetadataURL:       *metadataURL,
		AuthnNameIDFormat: saml.UnspecifiedNameIDFormat,
		AllowIDPInitiated: true,
	}

	// Attach the IdP metadata. Prefer MetadataURL (auto-refresh on demand);
	// fall back to the manual entity_id + sso_url + cert triple.
	if cfg.MetadataURL != "" {
		md, err := fetchIDPMetadata(cfg.MetadataURL)
		if err != nil {
			return nil, err
		}
		sp.IDPMetadata = md
	} else {
		if cfg.IDPEntityID == "" || cfg.SSOURL == "" || cfg.Certificate == "" {
			return nil, errors.New("saml provider requires either metadata_url or (idp_entity_id + sso_url + certificate)")
		}
		cert, err := parseCert(cfg.Certificate)
		if err != nil {
			return nil, err
		}
		sp.IDPMetadata = &saml.EntityDescriptor{
			EntityID: cfg.IDPEntityID,
			IDPSSODescriptors: []saml.IDPSSODescriptor{{
				SSODescriptor: saml.SSODescriptor{
					RoleDescriptor: saml.RoleDescriptor{
						KeyDescriptors: []saml.KeyDescriptor{{
							Use: "signing",
							KeyInfo: saml.KeyInfo{
								X509Data: saml.X509Data{
									X509Certificates: []saml.X509Certificate{{Data: base64FromPEM(cfg.Certificate)}},
								},
							},
						}},
					},
				},
				SingleSignOnServices: []saml.Endpoint{{
					Binding:  saml.HTTPRedirectBinding,
					Location: cfg.SSOURL,
				}},
			}},
		}
		_ = cert // parsed only to validate shape
	}

	// We do not sign AuthnRequests, so no SP key/cert is needed — leave the
	// SP.Key / SP.Certificate fields nil. crewjam/saml tolerates that and
	// produces unsigned requests, which most IdPs accept by default.
	return sp, nil
}

func fetchIDPMetadata(u string) (*saml.EntityDescriptor, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	resp, err := samlHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("metadata endpoint returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	md, err := samlsp.ParseMetadata(body)
	if err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}
	return md, nil
}

func parseCert(pemText string) (*x509.Certificate, error) {
	pemText = strings.TrimSpace(pemText)
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("certificate is not valid PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

// base64FromPEM strips PEM armour and returns the base64 body (which is
// exactly what goes inside <ds:X509Certificate>).
func base64FromPEM(pemText string) string {
	out := strings.Builder{}
	for _, line := range strings.Split(pemText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-----") {
			continue
		}
		out.WriteString(line)
	}
	return out.String()
}
