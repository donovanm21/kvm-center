package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// TLS management endpoints. All gated on the "tls.manage" permission
// except /api/tls/status which is readable by any authenticated user
// (the UI needs the expiry badge on the topbar).
//
//   GET  /api/tls/status        subject / issuer / expiry / fingerprint
//   POST /api/tls/upload        multipart cert+key, validated + atomic
//   POST /api/tls/regenerate    overwrite with a fresh self-signed pair
//   GET  /api/tls/cert.pem      download the current leaf cert (NOT the key)

type tlsStatusReply struct {
	Enabled           bool     `json:"enabled"`
	Subject           string   `json:"subject,omitempty"`
	Issuer            string   `json:"issuer,omitempty"`
	SelfSigned        bool     `json:"self_signed"`
	NotBefore         string   `json:"not_before,omitempty"`
	NotAfter          string   `json:"not_after,omitempty"`
	DaysUntilExpiry   int      `json:"days_until_expiry,omitempty"`
	FingerprintSHA256 string   `json:"fingerprint_sha256,omitempty"`
	DNSNames          []string `json:"dns_names,omitempty"`
	IPAddresses       []string `json:"ip_addresses,omitempty"`
	CertPath          string   `json:"cert_path,omitempty"`
	KeyPath           string   `json:"key_path,omitempty"`
	Error             string   `json:"error,omitempty"`
}

func handleTLSStatus(reloader *CertReloader, certPath, keyPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := tlsStatusReply{CertPath: certPath, KeyPath: keyPath}
		if reloader == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
		out.Enabled = true
		c, err := reloader.CurrentCert()
		if err != nil {
			out.Error = err.Error()
			writeJSON(w, http.StatusOK, out)
			return
		}
		out.Subject = c.Subject.String()
		out.Issuer = c.Issuer.String()
		out.SelfSigned = c.Subject.String() == c.Issuer.String()
		out.NotBefore = c.NotBefore.UTC().Format(time.RFC3339)
		out.NotAfter = c.NotAfter.UTC().Format(time.RFC3339)
		out.DaysUntilExpiry = int(time.Until(c.NotAfter).Hours() / 24)
		out.FingerprintSHA256 = CertFingerprintSHA256(c)
		out.DNSNames = c.DNSNames
		for _, ip := range c.IPAddresses {
			out.IPAddresses = append(out.IPAddresses, ip.String())
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleTLSUpload(certPath, keyPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		// Accept multipart (browser form) OR a JSON body {cert, key} —
		// the first is what the Settings UI sends, the second is handy
		// for scripted replacement.
		var certPEM, keyPEM []byte
		ct := r.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(ct, "multipart/form-data"):
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				writeError(w, http.StatusBadRequest, "bad multipart: "+err.Error())
				return
			}
			certPEM = readFormFile(r, "cert")
			keyPEM = readFormFile(r, "key")
		case strings.HasPrefix(ct, "application/json"):
			var body struct {
				Cert string `json:"cert"`
				Key  string `json:"key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				writeError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
				return
			}
			certPEM = []byte(body.Cert)
			keyPEM = []byte(body.Key)
		default:
			writeError(w, http.StatusUnsupportedMediaType,
				"Content-Type must be multipart/form-data or application/json")
			return
		}
		if len(certPEM) == 0 || len(keyPEM) == 0 {
			writeError(w, http.StatusBadRequest, "cert and key are both required")
			return
		}
		if err := AtomicWritePair(certPath, keyPath, certPEM, keyPEM); err != nil {
			writeError(w, http.StatusBadRequest, "invalid cert/key pair: "+err.Error())
			return
		}
		log.Printf("tls: operator uploaded new cert/key pair")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// auditTLSUpload / auditTLSRegenerate wrap the base handlers above with
// an audit-event side-effect so the Settings → TLS actions show up in
// the audit log. Kept as separate wrappers rather than inlined because
// the base handlers predate v5.2 and are still used in tests.
func auditTLSHandler(store *Store, action string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Record BEFORE calling to avoid losing the event if the write
		// process crashes mid-handshake.
		audit(store, r, action, "tls/server", nil, true, "")
		next(w, r)
	}
}

func handleTLSRegenerate(certPath, keyPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if err := GenerateSelfSigned(certPath, keyPath); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("tls: regenerated self-signed cert at %s", certPath)
		writeJSON(w, http.StatusOK, map[string]string{"status": "regenerated"})
	}
}

func handleTLSCertDownload(certPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(certPath)
		if err != nil {
			writeError(w, http.StatusNotFound, "cert not found")
			return
		}
		// Refuse to ship anything that isn't a CERTIFICATE PEM block —
		// guards against the file being corrupted or swapped with a key.
		block, _ := pem.Decode(b)
		if block == nil || block.Type != "CERTIFICATE" {
			writeError(w, http.StatusInternalServerError, "cert file is not a valid PEM certificate")
			return
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			writeError(w, http.StatusInternalServerError, "cert parse: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Header().Set("Content-Disposition", `attachment; filename="kvm-center.crt"`)
		_, _ = w.Write(b)
	}
}

func readFormFile(r *http.Request, key string) []byte {
	f, _, err := r.FormFile(key)
	if err != nil {
		if v := r.FormValue(key); v != "" {
			return []byte(v)
		}
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return b
}

// Sanity check: require() cert and key paths together. Either both are
// set (real or ensured self-signed) or neither.
var errTLSPathsIncomplete = errors.New("both -cert and -key must be set (or neither)")
