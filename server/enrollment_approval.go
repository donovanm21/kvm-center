package main

// Enrolment approval flow (v5.55.0).
//
// Mirrors the Discord-Gateway pair-request handshake (see
// server/discord_gateway.go) but for hypervisor PKI. Operator
// workflow:
//
//   1. Operator runs `install.sh --mode agent --host-id <id> --server <url>`
//      on the new hypervisor (no enrol token). install.sh generates
//      a CSR + ed25519 keypair and POSTs to /api/ca/enrollment-requests.
//   2. Server validates, stores the row in s.pendingEnrollments,
//      returns {id, poll_token, fingerprint, status="pending"}.
//   3. install.sh polls /api/ca/enrollment-requests/<id>?token=...
//      every 5s (loud fingerprint print so the operator can compare).
//   4. Operator opens Settings → TLS → Pending enrolments, verifies
//      fingerprint, clicks Approve.
//   5. Server signs the CSR via the existing ClusterCA path; stores
//      IssuedCertPEM + IssuedCAPEM in the row.
//   6. install.sh's next poll picks up the cert, writes it to
//      /etc/kvm-center/tls/, brings the agent up.
//   7. Cert is wiped from the row (DeliveredAt set) — one-shot.
//
// KVMC_AUTO_APPROVE_CIDR env opts a trusted subnet out of the
// approve step. Sources inside the CIDR get the cert returned
// on the SAME response as the initial POST. Useful for homelabs
// behind a flat LAN; off by default.
//
// Coexists with the v5.5+ token flow on /api/ca/sign — operators
// who want the scripted/CI path still mint an enrol token; the
// approval flow is the friendlier homelab default.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/models"
)

const (
	enrollmentPendingTTL = 1 * time.Hour
	enrollmentMaxPending = 50 // queue cap; rejects past this
	enrollmentRateBurst  = 30 // per-IP per minute
	enrollmentIDBytes    = 16
	pollTokenBytes       = 32
)

// Enrolment status values surfaced to the agent's poller + the UI.
const (
	enrollStatusPending   = "pending"
	enrollStatusApproved  = "approved"
	enrollStatusDelivered = "delivered"
	enrollStatusRejected  = "rejected"
	enrollStatusExpired   = "expired"
)

// Rate limiter — simple bucket per source IP. Goroutine pulls the
// gc loop. Lives at module scope so map state survives request
// goroutines.
var enrolLimiter = struct {
	mu   sync.Mutex
	hits map[string][]time.Time // ip → recent submission timestamps (rolling 1m)
}{hits: map[string][]time.Time{}}

func enrolRateLimitCheck(ip string) bool {
	now := time.Now()
	cutoff := now.Add(-1 * time.Minute)
	enrolLimiter.mu.Lock()
	defer enrolLimiter.mu.Unlock()
	hits := enrolLimiter.hits[ip]
	trimmed := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			trimmed = append(trimmed, t)
		}
	}
	if len(trimmed) >= enrollmentRateBurst {
		enrolLimiter.hits[ip] = trimmed
		return false
	}
	trimmed = append(trimmed, now)
	enrolLimiter.hits[ip] = trimmed
	return true
}

// ── store helpers ────────────────────────────────────────────────

func (s *Store) listPendingEnrollments() []*models.PendingEnrollment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.PendingEnrollment, 0, len(s.pendingEnrollments))
	for _, p := range s.pendingEnrollments {
		cp := *p
		// Defensive copy — strip cert + token hash from the listing
		// snapshot so a caller mutating the returned slice can't leak
		// either back to a future encoder.
		cp.PollTokenHash = ""
		cp.IssuedCertPEM = ""
		cp.IssuedCAPEM = ""
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt.After(out[j].SubmittedAt) })
	return out
}

func (s *Store) pendingEnrolmentCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, p := range s.pendingEnrollments {
		if p.Status == enrollStatusPending {
			n++
		}
	}
	return n
}

func (s *Store) storePendingEnrollment(p *models.PendingEnrollment) {
	s.mu.Lock()
	if s.pendingEnrollments == nil {
		s.pendingEnrollments = map[string]*models.PendingEnrollment{}
	}
	// v5.55.2 — supersede any prior pending entries for the same
	// host_id. Each install.sh invocation regenerates its keypair,
	// so older CSRs can't be paired with an agent-side private key
	// anyway. Without this, repeat install attempts accumulate in
	// the panel and force the operator to Reject the stale ones
	// before they can spot the live one.
	now := time.Now().UTC()
	for id, existing := range s.pendingEnrollments {
		if id == p.ID {
			continue
		}
		if existing == nil || existing.Status != enrollStatusPending {
			continue
		}
		if !strings.EqualFold(existing.HostID, p.HostID) {
			continue
		}
		existing.Status = enrollStatusRejected
		existing.RejectedBy = "auto-supersede"
		existing.RejectedAt = now
		existing.Reason = "superseded by a newer submission from the same host_id (" + p.ID + ")"
	}
	s.pendingEnrollments[p.ID] = p
	s.mu.Unlock()
	s.AsyncSave()
}

func (s *Store) getPendingEnrollment(id string) *models.PendingEnrollment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.pendingEnrollments[id]; ok {
		cp := *p
		return &cp
	}
	return nil
}

// patchPendingEnrollment mutates the live entry under the write lock.
// Used by approve/reject/sweeper paths.
func (s *Store) patchPendingEnrollment(id string, mut func(*models.PendingEnrollment)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pendingEnrollments[id]
	if !ok {
		return false
	}
	mut(p)
	// AsyncSave registers its work before starting its own goroutine.
	// An outer goroutine would let a shutdown/test drain miss this mutation.
	s.AsyncSave()
	return true
}

// deletePendingEnrollment hard-removes a row (used by the sweeper
// for entries that are both terminal AND past their retention
// window).
func (s *Store) deletePendingEnrollment(id string) {
	s.mu.Lock()
	delete(s.pendingEnrollments, id)
	s.mu.Unlock()
	s.AsyncSave()
}

// ── fingerprint helper ───────────────────────────────────────────

// pubkeyFingerprintFromCSR parses a PEM-encoded CSR and returns the
// SHA-256 of its public-key DER, formatted colon-hex uppercase. The
// operator compares this to what install.sh printed on the host —
// a fingerprint match is the trust anchor in the approval flow.
func pubkeyFingerprintFromCSR(csrPEM string) (string, error) {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		return "", fmt.Errorf("not a PEM block")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse CSR: %w", err)
	}
	pkDER, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil {
		return "", fmt.Errorf("marshal pubkey: %w", err)
	}
	sum := sha256.Sum256(pkDER)
	hexStr := strings.ToUpper(hex.EncodeToString(sum[:]))
	// Insert ':' every 2 chars.
	var b strings.Builder
	for i := 0; i < len(hexStr); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hexStr[i : i+2])
	}
	return b.String(), nil
}

// ── auto-approve CIDR ────────────────────────────────────────────

// autoApproveCIDR returns the parsed env CIDR, or nil if unset /
// invalid. Operators who run kvm-center on a flat LAN can opt
// trusted subnets out of the approve step.
func autoApproveCIDR() *net.IPNet {
	raw := strings.TrimSpace(os.Getenv("KVMC_AUTO_APPROVE_CIDR"))
	if raw == "" {
		return nil
	}
	_, n, err := net.ParseCIDR(raw)
	if err != nil {
		log.Printf("enrolment: KVMC_AUTO_APPROVE_CIDR=%q is not a valid CIDR — ignoring", raw)
		return nil
	}
	return n
}

// shouldAutoApprove returns true when source falls inside the
// trusted subnet.
func shouldAutoApprove(sourceIP string) bool {
	cidr := autoApproveCIDR()
	if cidr == nil {
		return false
	}
	ip := net.ParseIP(sourceIP)
	if ip == nil {
		return false
	}
	return cidr.Contains(ip)
}

// ── handler: POST /api/ca/enrollment-requests ────────────────────

// handleEnrolmentRequest is the new public entry point — no
// auth, rate-limited, queue-capped. New host POSTs CSR + identity
// claims, gets back an opaque ID + poll-token + computed
// fingerprint. The fingerprint is the trust anchor: install.sh
// prints it loudly on the host so the operator can compare in the
// approval UI.
func handleEnrolmentRequest(store *Store, ca *ClusterCA) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		ip := clientIP(r)
		if !enrolRateLimitCheck(ip) {
			writeError(w, http.StatusTooManyRequests, "enrolment request rate-limit exceeded; try again in a minute")
			return
		}
		if store.pendingEnrolmentCount() >= enrollmentMaxPending {
			writeError(w, http.StatusTooManyRequests, "pending enrolment queue is full; ask an operator to clear it or try again later")
			return
		}

		var req struct {
			HostID        string            `json:"host_id"`
			CSR           string            `json:"csr"`
			SourceVersion string            `json:"source_version,omitempty"`
			SystemInfo    map[string]string `json:"system_info,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		req.HostID = strings.TrimSpace(req.HostID)
		req.CSR = strings.TrimSpace(req.CSR)
		if req.HostID == "" || req.CSR == "" {
			writeError(w, http.StatusBadRequest, "host_id + csr required")
			return
		}
		if store.IsHostRemoved(req.HostID) {
			writeError(w, http.StatusForbidden, "host_id is on the retired list; clear it from Settings → TLS first")
			return
		}
		fp, err := pubkeyFingerprintFromCSR(req.CSR)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad CSR: "+err.Error())
			return
		}

		id := "enr_" + randHexShort(enrollmentIDBytes)
		pollToken := randHexShort(pollTokenBytes)
		tokenHash := sha256Hex(pollToken)

		p := &models.PendingEnrollment{
			ID:            id,
			HostID:        req.HostID,
			CSR:           req.CSR,
			Fingerprint:   fp,
			SourceIP:      ip,
			SourceVersion: req.SourceVersion,
			SystemInfo:    req.SystemInfo,
			SubmittedAt:   time.Now().UTC(),
			ExpiresAt:     time.Now().UTC().Add(enrollmentPendingTTL),
			Status:        enrollStatusPending,
			PollTokenHash: tokenHash,
		}

		// Auto-approve path: sign immediately, return the cert in the
		// same response so install.sh doesn't have to poll. Status
		// jumps straight to "delivered" — there's no operator step.
		autoApproved := shouldAutoApprove(ip)
		if autoApproved {
			extraDNS, extraIPs := buildHypervisorCertSANs(req.HostID, ip)
			certPEM, serial, fp, notBefore, notAfter, serr := ca.SignHypervisorCSR([]byte(req.CSR), req.HostID, extraDNS, extraIPs)
			if serr != nil {
				auditAs(store, r, "auto-approve", "enrolment", AuditCASign, "hypervisor/"+req.HostID, map[string]string{"reason": serr.Error()}, false, serr.Error())
				writeError(w, http.StatusBadRequest, "auto-approve sign failed: "+serr.Error())
				return
			}
			// v5.55.3 — record the issued cert in the hypervisor-cert
			// ledger so the Host.MTLSReady derivation flips true and
			// the Hypervisor PKI panel shows the entry. handleCASign
			// already does this for the token-driven path; the
			// approval flow had been forgetting to call it.
			store.recordHypervisorCert(&models.HypervisorCert{
				HostID:      req.HostID,
				SerialHex:   serial,
				Fingerprint: fp,
				Subject:     "CN=" + req.HostID,
				NotBefore:   notBefore,
				NotAfter:    notAfter,
				IssuedAt:    time.Now().UTC(),
				IssuedBy:    "auto-approve",
			})
			p.Status = enrollStatusDelivered
			p.ApprovedBy = "auto-approve"
			p.ApprovedAt = time.Now().UTC()
			p.AutoApproved = true
			p.Reason = "source_ip inside KVMC_AUTO_APPROVE_CIDR"
			p.IssuedSerial = serial
			p.IssuedNotAfter = notAfter
			p.DeliveredAt = time.Now().UTC()
			// Don't persist the cert when auto-approving — we return
			// it inline below and the row's terminal-state row holds
			// only metadata after that.
			store.storePendingEnrollment(p)
			auditAs(store, r, "auto-approve", "enrolment", AuditCASign, "hypervisor/"+req.HostID, map[string]string{
				"serial":    serial,
				"fp":        fp,
				"source_ip": ip,
			}, true, "")
			writeJSON(w, http.StatusOK, map[string]any{
				"id":            id,
				"poll_token":    pollToken,
				"fingerprint":   fp,
				"status":        enrollStatusDelivered,
				"auto_approved": true,
				"cert":          string(certPEM),
				"ca_cert":       string(ca.CertPEM()),
				"serial":        serial,
				"not_after":     notAfter.Format(time.RFC3339),
			})
			return
		}

		store.storePendingEnrollment(p)
		auditAs(store, r, "", "enrolment", AuditEnrolmentRequest, "hypervisor/"+req.HostID, map[string]string{
			"id":          id,
			"fingerprint": fp,
			"source_ip":   ip,
		}, true, "")

		// Fan-out to the gateway audit firehose (if enabled) so a
		// Discord card with the fingerprint surfaces immediately.
		// Card severity is "warn" so operators notice — it's an
		// action item, not informational.
		store.fireNotification(&models.NotificationEvent{
			Type:     "ca.enrolment.request",
			Title:    "Hypervisor enrolment pending approval — " + req.HostID,
			Message:  fmt.Sprintf("Source IP %s submitted a CSR. Verify the fingerprint matches what the agent printed, then Approve in Settings → TLS → Pending enrolments.", ip),
			Severity: "warn",
			Resource: "hypervisor/" + req.HostID,
			At:       time.Now().UTC(),
			Detail: map[string]string{
				"id":          id,
				"fingerprint": fp,
				"source_ip":   ip,
			},
		})

		writeJSON(w, http.StatusCreated, map[string]any{
			"id":          id,
			"poll_token":  pollToken,
			"fingerprint": fp,
			"status":      enrollStatusPending,
		})
	}
}

// ── handler: GET /api/ca/enrollment-requests/<id>?token= ─────────

// handleEnrolmentPoll is the agent-side status poller. Returns:
//
//	202 + {status:"pending"}          while awaiting operator approval
//	200 + {status:"approved", cert..} on first poll after approve (one-shot)
//	200 + {status:"delivered"}        on subsequent polls — agent stop polling
//	410 + {status:"rejected"|"expired"} terminal failure
//	404                                ID not found
//	401                                poll token didn't match
//
// Cert + ca_cert are returned exactly once. The row's
// IssuedCertPEM / IssuedCAPEM are wiped after a successful
// "approved" → "delivered" transition.
func handleEnrolmentPoll(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/ca/enrollment-poll/")
		if path == "" || strings.Contains(path, "/") {
			writeError(w, http.StatusBadRequest, "expected /api/ca/enrollment-poll/<id>")
			return
		}
		token := r.URL.Query().Get("token")
		if token == "" {
			writeError(w, http.StatusUnauthorized, "token query param required")
			return
		}

		p := store.getPendingEnrollment(path)
		if p == nil {
			writeError(w, http.StatusNotFound, "no such enrolment request")
			return
		}
		if sha256Hex(token) != p.PollTokenHash {
			writeError(w, http.StatusUnauthorized, "invalid poll token")
			return
		}

		switch p.Status {
		case enrollStatusPending:
			writeJSON(w, http.StatusAccepted, map[string]any{
				"id":          p.ID,
				"status":      p.Status,
				"fingerprint": p.Fingerprint,
				"expires_at":  p.ExpiresAt.Format(time.RFC3339),
			})
			return
		case enrollStatusApproved:
			// First poll after approve — return the cert one-shot,
			// then flip to delivered and wipe the cert from the row.
			cert := p.IssuedCertPEM
			caPEM := p.IssuedCAPEM
			store.patchPendingEnrollment(p.ID, func(x *models.PendingEnrollment) {
				x.Status = enrollStatusDelivered
				x.DeliveredAt = time.Now().UTC()
				x.IssuedCertPEM = ""
				x.IssuedCAPEM = ""
			})
			writeJSON(w, http.StatusOK, map[string]any{
				"id":        p.ID,
				"status":    enrollStatusDelivered,
				"cert":      cert,
				"ca_cert":   caPEM,
				"serial":    p.IssuedSerial,
				"not_after": p.IssuedNotAfter.Format(time.RFC3339),
			})
			return
		case enrollStatusDelivered:
			// Already-collected cert; nothing more to return.
			writeJSON(w, http.StatusOK, map[string]any{
				"id":     p.ID,
				"status": p.Status,
			})
			return
		case enrollStatusRejected, enrollStatusExpired:
			writeError(w, http.StatusGone, "enrolment request "+p.Status+": "+p.Reason)
			return
		default:
			writeError(w, http.StatusInternalServerError, "unknown status: "+p.Status)
		}
	}
}

// ── handler: list / approve / reject ─────────────────────────────

func handleEnrolmentList(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		writeJSON(w, http.StatusOK, store.listPendingEnrollments())
	}
}

func handleEnrolmentApprove(store *Store, ca *ClusterCA) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/ca/enrollment-requests/")
		id := strings.TrimSuffix(path, "/approve")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusBadRequest, "expected /api/ca/enrollment-requests/<id>/approve")
			return
		}
		p := store.getPendingEnrollment(id)
		if p == nil {
			writeError(w, http.StatusNotFound, "no such enrolment request")
			return
		}
		if p.Status != enrollStatusPending {
			writeError(w, http.StatusConflict, "enrolment is in terminal state: "+p.Status)
			return
		}
		if store.IsHostRemoved(p.HostID) {
			writeError(w, http.StatusForbidden, "host_id has been retired since the request was filed; clear it from Settings → TLS first")
			return
		}
		extraDNS, extraIPs := buildHypervisorCertSANs(p.HostID, p.SourceIP)
		certPEM, serial, fp, notBefore, notAfter, serr := ca.SignHypervisorCSR([]byte(p.CSR), p.HostID, extraDNS, extraIPs)
		if serr != nil {
			audit(store, r, AuditEnrolmentApprove, "hypervisor/"+p.HostID, map[string]string{"id": id, "reason": serr.Error()}, false, serr.Error())
			writeError(w, http.StatusBadRequest, "sign failed: "+serr.Error())
			return
		}
		actor := ""
		if u := userFromRequest(r); u != nil {
			actor = u.ID
		}
		// v5.55.3 — record the issued cert in the hypervisor-cert
		// ledger (the same store handleCASign uses for the token
		// path). Without this the Host.MTLSReady derivation stays
		// false and the Hypervisor PKI panel is missing the entry.
		store.recordHypervisorCert(&models.HypervisorCert{
			HostID:      p.HostID,
			SerialHex:   serial,
			Fingerprint: fp,
			Subject:     "CN=" + p.HostID,
			NotBefore:   notBefore,
			NotAfter:    notAfter,
			IssuedAt:    time.Now().UTC(),
			IssuedBy:    actor,
		})
		store.patchPendingEnrollment(id, func(x *models.PendingEnrollment) {
			x.Status = enrollStatusApproved
			x.ApprovedBy = actor
			x.ApprovedAt = time.Now().UTC()
			x.IssuedSerial = serial
			x.IssuedNotAfter = notAfter
			x.IssuedCertPEM = string(certPEM)
			x.IssuedCAPEM = string(ca.CertPEM())
		})
		approveDetail := map[string]string{
			"id":          id,
			"fingerprint": p.Fingerprint,
			"serial":      serial,
		}
		audit(store, r, AuditEnrolmentApprove, "hypervisor/"+p.HostID, approveDetail, true, "")
		// Use host/<id> for Recent Tasks so it groups with the host's
		// other operator activity (retire, maintenance, etc.).
		recordOperatorTask(store, r, AuditEnrolmentApprove, "host/"+p.HostID, approveDetail, true, "")
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     id,
			"status": enrollStatusApproved,
		})
	}
}

func handleEnrolmentReject(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/ca/enrollment-requests/")
		id := strings.TrimSuffix(path, "/reject")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusBadRequest, "expected /api/ca/enrollment-requests/<id>/reject")
			return
		}
		var body struct {
			Reason string `json:"reason,omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		p := store.getPendingEnrollment(id)
		if p == nil {
			writeError(w, http.StatusNotFound, "no such enrolment request")
			return
		}
		if p.Status != enrollStatusPending {
			writeError(w, http.StatusConflict, "enrolment is in terminal state: "+p.Status)
			return
		}
		actor := ""
		if u := userFromRequest(r); u != nil {
			actor = u.ID
		}
		store.patchPendingEnrollment(id, func(x *models.PendingEnrollment) {
			x.Status = enrollStatusRejected
			x.RejectedBy = actor
			x.RejectedAt = time.Now().UTC()
			x.Reason = body.Reason
		})
		rejectDetail := map[string]string{"id": id, "reason": body.Reason}
		audit(store, r, AuditEnrolmentReject, "hypervisor/"+p.HostID, rejectDetail, true, "")
		recordOperatorTask(store, r, AuditEnrolmentReject, "host/"+p.HostID, rejectDetail, true, "")
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": enrollStatusRejected})
	}
}

// ── sweeper ──────────────────────────────────────────────────────

// startEnrolmentSweeper runs once a minute. Marks pending entries
// past their TTL as "expired" and hard-removes terminal rows older
// than 24h so the list doesn't grow unbounded.
func (s *Store) startEnrolmentSweeper(stop <-chan struct{}) {
	s.goWork("enrollment_approval", func() {
		t := time.NewTicker(1 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.sweepEnrolments()
			}
		}
	})
}

func (s *Store) sweepEnrolments() {
	now := time.Now()
	s.mu.Lock()
	type sweepAction struct {
		id     string
		expire bool
		remove bool
	}
	var actions []sweepAction
	for id, p := range s.pendingEnrollments {
		if p.Status == enrollStatusPending && now.After(p.ExpiresAt) {
			actions = append(actions, sweepAction{id: id, expire: true})
			continue
		}
		// Hard-remove terminal entries past 24h so the list doesn't
		// accumulate forever.
		switch p.Status {
		case enrollStatusDelivered, enrollStatusRejected, enrollStatusExpired:
			anchor := p.DeliveredAt
			if anchor.IsZero() {
				anchor = p.RejectedAt
			}
			if anchor.IsZero() {
				anchor = p.SubmittedAt
			}
			if now.Sub(anchor) > 24*time.Hour {
				actions = append(actions, sweepAction{id: id, remove: true})
			}
		}
	}
	for _, a := range actions {
		if a.expire {
			p := s.pendingEnrollments[a.id]
			if p != nil {
				p.Status = enrollStatusExpired
				p.Reason = "no operator action within " + enrollmentPendingTTL.String()
			}
		}
		if a.remove {
			delete(s.pendingEnrollments, a.id)
		}
	}
	s.mu.Unlock()
	if len(actions) > 0 {
		s.AsyncSave()
	}
}

// ── helpers ──────────────────────────────────────────────────────

func randHexShort(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// buildHypervisorCertSANs derives the SAN list to attach when
// signing a hypervisor's CSR. Returns (dnsNames, ipAddrs) suitable
// for passing to ClusterCA.SignHypervisorCSR.
//
// Pre-v5.58.2 the approval flow passed nil/nil here, which produced
// certs with only the CN-as-SAN (the FQDN). libvirt's TLS migration
// path then failed peer verification when the migrate-uri used the
// host's IP, because the cert had no IP SAN to match. The
// token-driven /api/ca/sign path filled this in from request
// metadata; the approval flow forgot to. Symptom surfaced during
// hostD dogfood: migrations FROM hostA TO hostD failed
// with "Certificate [session] owner does not match the hostname
// 10.0.0.14".
//
// Returned lists:
//   - DNS: short form of host_id (e.g. "hostD" when host_id is
//     "hostD.example.com") so peers can connect by either name
//   - IP: the source IP the CSR was submitted from (the agent's
//     management-VLAN address that other agents reach it on)
func buildHypervisorCertSANs(hostID, sourceIP string) (dnsNames, ipAddrs []string) {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return nil, nil
	}
	// Short hostname is everything before the first dot. Skip if
	// the host_id was already short (no dot present).
	if i := strings.Index(hostID, "."); i > 0 {
		short := hostID[:i]
		if short != "" && short != hostID {
			dnsNames = append(dnsNames, short)
		}
	}
	sourceIP = strings.TrimSpace(sourceIP)
	if sourceIP != "" {
		ipAddrs = append(ipAddrs, sourceIP)
	}
	return dnsNames, ipAddrs
}
