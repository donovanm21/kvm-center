package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"kvm-center/internal/models"
)

// Hypervisor PKI HTTP surface (v5.10.0).
//
//   POST /api/ca/enroll           admin — mint one-time bootstrap token for a hypervisor
//   POST /api/ca/sign             public (token-gated) — exchange CSR for signed leaf
//   GET  /api/ca/bundle           any signed-in user — fetch CA public cert
//   GET  /api/ca/hypervisors      admin — list issued hypervisor certs
//   POST /api/ca/hypervisors/<id>/revoke  admin — mark a cert revoked

const (
	enrollTokenTTL = 10 * time.Minute

	AuditCAEnroll = "ca.enroll"
	AuditCASign   = "ca.sign"
	AuditCARevoke = "ca.revoke"
)

// caEnrollEntry is an in-flight bootstrap token. TTL-bounded so a
// token that isn't redeemed quickly falls out on its own.
type caEnrollEntry struct {
	HostID    string
	ExpiresAt time.Time
	IssuedBy  string
}

func (s *Store) mintEnrollToken(hostID, issuedBy string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	tok := "enroll_" + hex.EncodeToString(b[:])
	s.mu.Lock()
	if s.caEnrollTokens == nil {
		s.caEnrollTokens = map[string]*caEnrollEntry{}
	}
	s.caEnrollTokens[tok] = &caEnrollEntry{
		HostID:    hostID,
		ExpiresAt: time.Now().Add(enrollTokenTTL),
		IssuedBy:  issuedBy,
	}
	s.mu.Unlock()
	return tok
}

// consumeEnrollToken validates and removes a token, returning the
// bound host ID. Cleans up every expired entry under the same lock
// to keep the map bounded without a background reaper.
func (s *Store) consumeEnrollToken(tok string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.caEnrollTokens {
		if now.After(v.ExpiresAt) {
			delete(s.caEnrollTokens, k)
		}
	}
	entry, ok := s.caEnrollTokens[tok]
	if !ok {
		return "", "", errors.New("enrollment token invalid or expired")
	}
	delete(s.caEnrollTokens, tok)
	return entry.HostID, entry.IssuedBy, nil
}

func (s *Store) recordHypervisorCert(rec *models.HypervisorCert) {
	s.mu.Lock()
	s.hypervisorCerts[rec.HostID] = rec
	s.mu.Unlock()
	go s.Save()
}

func (s *Store) listHypervisorCerts() []*models.HypervisorCert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.HypervisorCert, 0, len(s.hypervisorCerts))
	for _, c := range s.hypervisorCerts {
		cp := *c
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IssuedAt.After(out[j].IssuedAt) })
	return out
}

func (s *Store) revokeHypervisorCert(hostID, actor, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.hypervisorCerts[hostID]
	if !ok {
		return errors.New("no cert for host")
	}
	if !c.RevokedAt.IsZero() {
		return errors.New("already revoked")
	}
	c.RevokedAt = time.Now().UTC()
	c.RevokedBy = actor
	c.RevokeReason = reason
	go s.Save()
	return nil
}

// ── HTTP handlers ──────────────────────────────────────────────────

// handleCAEnroll — admin creates a bootstrap token for hostID.
func handleCAEnroll(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			HostID string `json:"host_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		req.HostID = strings.TrimSpace(req.HostID)
		if req.HostID == "" {
			writeError(w, http.StatusBadRequest, "host_id required")
			return
		}
		u := userFromRequest(r)
		actorID := ""
		if u != nil {
			actorID = u.ID
		}
		tok := store.mintEnrollToken(req.HostID, actorID)
		audit(store, r, AuditCAEnroll, "hypervisor/"+req.HostID, map[string]string{"ttl": enrollTokenTTL.String()}, true, "")
		writeJSON(w, http.StatusOK, map[string]any{
			"enroll_token": tok,
			"host_id":      req.HostID,
			"expires_in":   int(enrollTokenTTL.Seconds()),
		})
	}
}

// handleCASign — public endpoint gated by the one-time bootstrap
// token. Caller POSTs { enroll_token, csr } and receives
// { cert, ca_cert, serial, not_after }. The subject CN is overridden
// with the host ID that was bound to the enrollment token — the
// agent can't choose its own identity.
func handleCASign(store *Store, ca *ClusterCA) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var req struct {
			EnrollToken string   `json:"enroll_token"`
			CSR         string   `json:"csr"` // PEM
			DNSNames    []string `json:"dns_names,omitempty"`
			IPAddresses []string `json:"ip_addresses,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		hostID, issuedBy, err := store.consumeEnrollToken(req.EnrollToken)
		if err != nil {
			auditAs(store, r, "", "enrollment", AuditCASign, "hypervisor/?", map[string]string{"reason": err.Error()}, false, err.Error())
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		// Automatically include the host's known IP in the SAN list so
		// peers can connect by IP without the hostname check failing.
		// The Host record is authoritative — anything the enrolling
		// agent claims via req.IPAddresses is appended in addition.
		ipAddrs := append([]string{}, req.IPAddresses...)
		if h := store.GetHost(hostID); h != nil && h.IP != "" {
			ipAddrs = append(ipAddrs, h.IP)
		}
		// Also drop the FQDN for this host if the operator configured a
		// cluster DNS domain via KVM_CENTER_CLUSTER_DOMAIN (e.g.
		// "don.homelab" → cert SAN includes "<hostID>.don.homelab").
		dnsNames := append([]string{}, req.DNSNames...)
		if dom := strings.TrimSpace(os.Getenv("KVM_CENTER_CLUSTER_DOMAIN")); dom != "" {
			dnsNames = append(dnsNames, hostID+"."+strings.TrimPrefix(dom, "."))
		}
		certPEM, serial, fp, notBefore, notAfter, serr := ca.SignHypervisorCSR([]byte(req.CSR), hostID, dnsNames, ipAddrs)
		if serr != nil {
			auditAs(store, r, issuedBy, "enrollment", AuditCASign, "hypervisor/"+hostID, nil, false, serr.Error())
			writeError(w, http.StatusBadRequest, serr.Error())
			return
		}
		rec := &models.HypervisorCert{
			HostID:      hostID,
			SerialHex:   serial,
			Fingerprint: fp,
			Subject:     "CN=" + hostID,
			NotBefore:   notBefore,
			NotAfter:    notAfter,
			IssuedAt:    time.Now().UTC(),
			IssuedBy:    issuedBy,
		}
		store.recordHypervisorCert(rec)
		auditAs(store, r, issuedBy, "enrollment", AuditCASign, "hypervisor/"+hostID, map[string]string{
			"serial":      serial,
			"fingerprint": fp,
			"not_after":   notAfter.Format(time.RFC3339),
		}, true, "")
		writeJSON(w, http.StatusOK, map[string]any{
			"cert":      string(certPEM),
			"ca_cert":   string(ca.CertPEM()),
			"serial":    serial,
			"not_after": notAfter.Format(time.RFC3339),
		})
	}
}

func handleCABundle(ca *ClusterCA) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Write(ca.CertPEM())
	}
}

func handleCAHypervisors(store *Store, ca *ClusterCA) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		caCert := ca.Cert()
		writeJSON(w, http.StatusOK, map[string]any{
			"certs": store.listHypervisorCerts(),
			"ca": map[string]any{
				"subject":    caCert.Subject.String(),
				"not_before": caCert.NotBefore.Format(time.RFC3339),
				"not_after":  caCert.NotAfter.Format(time.RFC3339),
			},
		})
	}
}

func handleCARevoke(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		// Path: /api/ca/hypervisors/<id>/revoke
		rest := strings.TrimPrefix(r.URL.Path, "/api/ca/hypervisors/")
		hostID := strings.TrimSuffix(rest, "/revoke")
		if hostID == "" || hostID == rest {
			writeError(w, http.StatusBadRequest, "host id required")
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		u := userFromRequest(r)
		actorID := ""
		if u != nil {
			actorID = u.ID
		}
		if err := store.revokeHypervisorCert(hostID, actorID, req.Reason); err != nil {
			audit(store, r, AuditCARevoke, "hypervisor/"+hostID, nil, false, err.Error())
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		audit(store, r, AuditCARevoke, "hypervisor/"+hostID, map[string]string{"reason": req.Reason}, true, "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
