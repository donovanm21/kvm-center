package main

// Enrolment approval flow tests (v5.55.0). Pin the surface
// behaviour of the new public endpoints + the auto-approve CIDR
// path. Lower-level CSR signing is already covered by ca_test.go.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kvm-center/internal/models"
)

func genTestCSR(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}
	der, err := x509.CreateCertificateRequest(rand.Reader, &tpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func newTestCA(t *testing.T) *ClusterCA {
	t.Helper()
	tmp := t.TempDir()
	ca, err := EnsureClusterCA(tmp)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func TestEnrolment_SubmitProducesPending(t *testing.T) {
	store := newTestStore(t)
	ca := newTestCA(t)
	h := handleEnrolmentRequest(store, ca)

	body, _ := json.Marshal(map[string]string{
		"host_id": "hvtest.lan",
		"csr":     genTestCSR(t, "hvtest.lan"),
	})
	r := httptest.NewRequest(http.MethodPost, "/api/ca/enrollment-requests", strings.NewReader(string(body)))
	r.RemoteAddr = "203.0.113.5:54321"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		ID          string `json:"id"`
		PollToken   string `json:"poll_token"`
		Fingerprint string `json:"fingerprint"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID == "" || out.PollToken == "" {
		t.Errorf("missing id/token in response: %+v", out)
	}
	if out.Status != enrollStatusPending {
		t.Errorf("status: got %q want pending", out.Status)
	}
	if !strings.Contains(out.Fingerprint, ":") {
		t.Errorf("fingerprint should be colon-hex: %q", out.Fingerprint)
	}
	if got := store.pendingEnrolmentCount(); got != 1 {
		t.Errorf("pendingEnrolmentCount: got %d", got)
	}
}

func TestEnrolment_PollPendingReturns202(t *testing.T) {
	store := newTestStore(t)
	ca := newTestCA(t)
	body, _ := json.Marshal(map[string]string{
		"host_id": "hvtest2.lan",
		"csr":     genTestCSR(t, "hvtest2.lan"),
	})
	r := httptest.NewRequest(http.MethodPost, "/api/ca/enrollment-requests", strings.NewReader(string(body)))
	r.RemoteAddr = "203.0.113.6:54321"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleEnrolmentRequest(store, ca)(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		ID        string `json:"id"`
		PollToken string `json:"poll_token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)

	// Poll while still pending.
	pollR := httptest.NewRequest(http.MethodGet, "/api/ca/enrollment-poll/"+out.ID+"?token="+out.PollToken, nil)
	pollW := httptest.NewRecorder()
	handleEnrolmentPoll(store)(pollW, pollR)
	if pollW.Code != http.StatusAccepted {
		t.Errorf("poll pending: got %d want 202; body=%s", pollW.Code, pollW.Body.String())
	}
}

func TestEnrolment_ApproveDeliversCertOneShot(t *testing.T) {
	store := newTestStore(t)
	ca := newTestCA(t)
	csr := genTestCSR(t, "hvtest3.lan")

	body, _ := json.Marshal(map[string]string{"host_id": "hvtest3.lan", "csr": csr})
	r := httptest.NewRequest(http.MethodPost, "/api/ca/enrollment-requests", strings.NewReader(string(body)))
	r.RemoteAddr = "203.0.113.7:54321"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleEnrolmentRequest(store, ca)(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	var subOut struct {
		ID        string `json:"id"`
		PollToken string `json:"poll_token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &subOut)

	// Approve.
	apR := httptest.NewRequest(http.MethodPost, "/api/ca/enrollment-requests/"+subOut.ID+"/approve", nil)
	apW := httptest.NewRecorder()
	handleEnrolmentApprove(store, ca)(apW, apR)
	if apW.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", apW.Code, apW.Body.String())
	}

	// First poll after approve: should return 200 + cert.
	p1R := httptest.NewRequest(http.MethodGet, "/api/ca/enrollment-poll/"+subOut.ID+"?token="+subOut.PollToken, nil)
	p1W := httptest.NewRecorder()
	handleEnrolmentPoll(store)(p1W, p1R)
	if p1W.Code != http.StatusOK {
		t.Fatalf("first poll: %d %s", p1W.Code, p1W.Body.String())
	}
	var pollOut struct {
		Status string `json:"status"`
		Cert   string `json:"cert"`
		CACert string `json:"ca_cert"`
	}
	_ = json.Unmarshal(p1W.Body.Bytes(), &pollOut)
	if pollOut.Cert == "" || pollOut.CACert == "" {
		t.Error("expected cert + ca_cert on first poll after approve")
	}
	if pollOut.Status != enrollStatusDelivered {
		t.Errorf("status: got %q want delivered", pollOut.Status)
	}

	// Second poll: status delivered, no cert (one-shot).
	p2R := httptest.NewRequest(http.MethodGet, "/api/ca/enrollment-poll/"+subOut.ID+"?token="+subOut.PollToken, nil)
	p2W := httptest.NewRecorder()
	handleEnrolmentPoll(store)(p2W, p2R)
	if p2W.Code != http.StatusOK {
		t.Fatalf("second poll: %d", p2W.Code)
	}
	// Use a fresh struct so a missing cert field doesn't leave the
	// old value from the first poll's unmarshal.
	var poll2 struct {
		Status string `json:"status"`
		Cert   string `json:"cert"`
	}
	_ = json.Unmarshal(p2W.Body.Bytes(), &poll2)
	if poll2.Cert != "" {
		t.Error("cert should be wiped after first successful poll")
	}
}

func TestEnrolment_PollWithBadTokenRejects(t *testing.T) {
	store := newTestStore(t)
	ca := newTestCA(t)
	body, _ := json.Marshal(map[string]string{"host_id": "hvtest4.lan", "csr": genTestCSR(t, "hvtest4.lan")})
	r := httptest.NewRequest(http.MethodPost, "/api/ca/enrollment-requests", strings.NewReader(string(body)))
	r.RemoteAddr = "203.0.113.8:54321"
	w := httptest.NewRecorder()
	handleEnrolmentRequest(store, ca)(w, r)
	var subOut struct{ ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &subOut)

	pollR := httptest.NewRequest(http.MethodGet, "/api/ca/enrollment-poll/"+subOut.ID+"?token=wrong", nil)
	pollW := httptest.NewRecorder()
	handleEnrolmentPoll(store)(pollW, pollR)
	if pollW.Code != http.StatusUnauthorized {
		t.Errorf("bad-token poll: got %d want 401", pollW.Code)
	}
}

func TestEnrolment_RetiredHostRejectsSubmit(t *testing.T) {
	store := newTestStore(t)
	ca := newTestCA(t)
	store.MarkHostRemoved("hvretired.lan", "u1", "retired")

	body, _ := json.Marshal(map[string]string{"host_id": "hvretired.lan", "csr": genTestCSR(t, "hvretired.lan")})
	r := httptest.NewRequest(http.MethodPost, "/api/ca/enrollment-requests", strings.NewReader(string(body)))
	r.RemoteAddr = "203.0.113.9:54321"
	w := httptest.NewRecorder()
	handleEnrolmentRequest(store, ca)(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("retired host: got %d want 403; body=%s", w.Code, w.Body.String())
	}
}

func TestEnrolment_AutoApproveCIDRSignsInline(t *testing.T) {
	t.Setenv("KVMC_AUTO_APPROVE_CIDR", "10.0.0.0/8")
	store := newTestStore(t)
	ca := newTestCA(t)

	body, _ := json.Marshal(map[string]string{"host_id": "hvauto.lan", "csr": genTestCSR(t, "hvauto.lan")})
	r := httptest.NewRequest(http.MethodPost, "/api/ca/enrollment-requests", strings.NewReader(string(body)))
	r.RemoteAddr = "10.0.0.5:54321"
	w := httptest.NewRecorder()
	handleEnrolmentRequest(store, ca)(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("auto-approve: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Status       string
		AutoApproved bool `json:"auto_approved"`
		Cert         string
		CACert       string `json:"ca_cert"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !out.AutoApproved {
		t.Error("auto_approved should be true")
	}
	if out.Status != enrollStatusDelivered {
		t.Errorf("status: got %q want delivered", out.Status)
	}
	if out.Cert == "" || out.CACert == "" {
		t.Error("auto-approve should return cert + ca inline")
	}
}

func TestEnrolment_SecondSubmitForSameHostSupersedes(t *testing.T) {
	store := newTestStore(t)
	ca := newTestCA(t)
	hostID := "hvtwice.lan"

	// First submit.
	body1, _ := json.Marshal(map[string]string{"host_id": hostID, "csr": genTestCSR(t, hostID)})
	r1 := httptest.NewRequest(http.MethodPost, "/api/ca/enrollment-requests", strings.NewReader(string(body1)))
	r1.RemoteAddr = "203.0.113.11:54321"
	w1 := httptest.NewRecorder()
	handleEnrolmentRequest(store, ca)(w1, r1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first submit: %d", w1.Code)
	}
	var first struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w1.Body.Bytes(), &first)

	// Second submit for the same host — should supersede the first.
	body2, _ := json.Marshal(map[string]string{"host_id": hostID, "csr": genTestCSR(t, hostID)})
	r2 := httptest.NewRequest(http.MethodPost, "/api/ca/enrollment-requests", strings.NewReader(string(body2)))
	r2.RemoteAddr = "203.0.113.11:54321"
	w2 := httptest.NewRecorder()
	handleEnrolmentRequest(store, ca)(w2, r2)
	if w2.Code != http.StatusCreated {
		t.Fatalf("second submit: %d", w2.Code)
	}
	var second struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &second)

	if second.ID == first.ID {
		t.Fatal("expected distinct IDs")
	}
	if got := store.getPendingEnrollment(first.ID); got == nil || got.Status != enrollStatusRejected {
		t.Errorf("first entry should be auto-rejected, got status=%q", func() string {
			if got == nil {
				return "missing"
			}
			return got.Status
		}())
	}
	if got := store.getPendingEnrollment(first.ID); got != nil && got.RejectedBy != "auto-supersede" {
		t.Errorf("first entry rejected_by should be auto-supersede, got %q", got.RejectedBy)
	}
	if got := store.getPendingEnrollment(second.ID); got == nil || got.Status != enrollStatusPending {
		t.Error("second entry should be pending")
	}
	if got := store.pendingEnrolmentCount(); got != 1 {
		t.Errorf("pendingEnrolmentCount: got %d want 1 (after supersede)", got)
	}
}

func TestEnrolment_Sweeper_ExpiresPending(t *testing.T) {
	store := newTestStore(t)
	ca := newTestCA(t)
	body, _ := json.Marshal(map[string]string{"host_id": "hvexp.lan", "csr": genTestCSR(t, "hvexp.lan")})
	r := httptest.NewRequest(http.MethodPost, "/api/ca/enrollment-requests", strings.NewReader(string(body)))
	r.RemoteAddr = "203.0.113.10:54321"
	w := httptest.NewRecorder()
	handleEnrolmentRequest(store, ca)(w, r)
	var subOut struct{ ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &subOut)

	// Backdate ExpiresAt manually so sweepEnrolments classifies as expired.
	store.patchPendingEnrollment(subOut.ID, func(p *models.PendingEnrollment) {
		p.ExpiresAt = time.Now().Add(-1 * time.Minute)
	})
	store.sweepEnrolments()

	got := store.getPendingEnrollment(subOut.ID)
	if got == nil {
		t.Fatal("entry should still exist after sweep (just expired, not removed)")
	}
	if got.Status != enrollStatusExpired {
		t.Errorf("status after sweep: got %q want expired", got.Status)
	}
}
