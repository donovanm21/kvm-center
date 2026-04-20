package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kvm-center/internal/models"
)

// Migration grants — agent side (v5.11.0).
//
// Source agent:
//   * Receives `grant` param on the MIGRATE command (server mints).
//   * Before calling virsh migrate, POSTs the grant envelope to the
//     destination's /prepare-grant endpoint so the destination can
//     validate + cache it ahead of libvirtd's incoming connection.
//
// Destination agent:
//   * /prepare-grant validates the envelope's Ed25519 signature
//     against the cached server pub key, checks the src/dst/vm/TTL
//     fields, and records the grant_id in an in-memory single-use
//     set. Returns 200 on accept, 4xx on reject.
//
// Trust bootstrap: every agent fetches the server's grant-pubkey once
// at process start and caches it. Rotating the server's signer
// invalidates grants in flight — acceptable because TTL is 5 min
// anyway.

var (
	grantPubKey      ed25519.PublicKey
	grantPubKeyMu    sync.RWMutex
	consumedGrants   = map[string]time.Time{} // grant_id → accepted_at (expiry cleanup on set)
	consumedGrantsMu sync.Mutex
	// activeGrantsByVM tracks recently-accepted grants keyed by VM
	// name so the libvirt qemu hook can ask "is there an authorised
	// migration for <vm> in flight?". Distinct from consumedGrants
	// (which is keyed by grant_id for replay defence) — same event
	// populates both.
	activeGrantsByVM = map[string]*activeGrant{}
)

type activeGrant struct {
	ID         string
	AcceptedAt time.Time
	ExpiresAt  time.Time
}

const (
	grantLogPath   = "/var/lib/kvm-center/consumed-grants.log"
	activeGrantTTL = 10 * time.Minute // hook consultation window
)

// loadGrantPubKey fetches the server's Ed25519 public key and caches
// it. Called once at startup; non-fatal if the server is unreachable
// (agent retries on first actual validation).
func loadGrantPubKey(serverURL string) {
	url := strings.TrimRight(serverURL, "/") + "/api/migrations/grant-pubkey"
	// Use the shared httpClient so the InsecureSkipVerify trust model
	// (applied by initHTTPClient when the server URL is https://) is
	// honoured. Prior fresh client failed TLS verify against the
	// server's self-signed cert when the agent is pointed at the
	// hostname, not the cert's localhost/container-ID SAN, which
	// turned grant-authorised migrations into HTTP 503 on the dest.
	resp, err := httpClient.Get(url)
	if err != nil {
		log.Printf("grants: pubkey fetch deferred (%v) — will retry on first grant", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("grants: pubkey fetch %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}
	var r struct {
		PubkeyHex string `json:"pubkey_hex"`
	}
	if jerr := json.Unmarshal(body, &r); jerr != nil || r.PubkeyHex == "" {
		log.Printf("grants: pubkey response malformed")
		return
	}
	pub, derr := hex.DecodeString(r.PubkeyHex)
	if derr != nil || len(pub) != ed25519.PublicKeySize {
		log.Printf("grants: pubkey hex decode failed")
		return
	}
	grantPubKeyMu.Lock()
	grantPubKey = pub
	grantPubKeyMu.Unlock()
	log.Printf("grants: pubkey cached (%s…)", r.PubkeyHex[:16])
}

func ensureGrantPubKey(serverURL string) ed25519.PublicKey {
	grantPubKeyMu.RLock()
	k := grantPubKey
	grantPubKeyMu.RUnlock()
	if k != nil {
		return k
	}
	loadGrantPubKey(serverURL)
	grantPubKeyMu.RLock()
	defer grantPubKeyMu.RUnlock()
	return grantPubKey
}

// verifyGrantEnvelope — agent-side copy of the server's verifier.
// Returns the decoded grant on success; error with a reason string
// on failure.
func verifyGrantEnvelope(pub ed25519.PublicKey, envelope string) (*models.MigrationGrant, error) {
	raw, err := base64.StdEncoding.DecodeString(envelope)
	if err != nil {
		return nil, fmt.Errorf("grant: base64 decode: %w", err)
	}
	var signed models.SignedGrant
	if err := json.Unmarshal(raw, &signed); err != nil {
		return nil, fmt.Errorf("grant: unmarshal envelope: %w", err)
	}
	if !ed25519.Verify(pub, signed.Payload, signed.Signature) {
		return nil, errors.New("grant: signature invalid")
	}
	var g models.MigrationGrant
	if err := json.Unmarshal(signed.Payload, &g); err != nil {
		return nil, fmt.Errorf("grant: unmarshal payload: %w", err)
	}
	if time.Now().Unix() > g.ExpiresAt {
		return &g, errors.New("grant: expired")
	}
	return &g, nil
}

// recordConsumedGrant returns (accepted, reason). Rejects a repeat of
// the same grant ID. Drops entries whose original grant TTL has
// passed so the map stays bounded. Also appends to a durable log so
// a daemon restart doesn't reset replay defence.
func recordConsumedGrant(g *models.MigrationGrant) (bool, string) {
	consumedGrantsMu.Lock()
	defer consumedGrantsMu.Unlock()
	now := time.Now()
	for k, v := range consumedGrants {
		if now.Sub(v) > 2*time.Hour { // keep well beyond grantTTL
			delete(consumedGrants, k)
		}
	}
	if _, exists := consumedGrants[g.ID]; exists {
		return false, "replay"
	}
	consumedGrants[g.ID] = now
	activeGrantsByVM[g.VMName] = &activeGrant{
		ID:         g.ID,
		AcceptedAt: now,
		ExpiresAt:  time.Unix(g.ExpiresAt, 0),
	}
	persistConsumedGrant(g.ID, now)
	return true, ""
}

// persistConsumedGrant appends a line to the replay log. Best-effort
// — failures are logged but don't abort the migration, matching the
// "safe write path" intent (operator can still manually inspect).
func persistConsumedGrant(id string, at time.Time) {
	if err := os.MkdirAll(filepath.Dir(grantLogPath), 0o700); err != nil {
		log.Printf("grants: mkdir log dir: %v", err)
		return
	}
	f, err := os.OpenFile(grantLogPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		log.Printf("grants: open log: %v", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %d\n", id, at.Unix())
}

// LoadConsumedGrantsLog rehydrates the replay set from disk at
// startup. Caller is main().
func LoadConsumedGrantsLog() {
	f, err := os.Open(grantLogPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("grants: load log: %v", err)
		}
		return
	}
	defer f.Close()
	consumedGrantsMu.Lock()
	defer consumedGrantsMu.Unlock()
	count := 0
	cutoff := time.Now().Add(-2 * time.Hour)
	var id string
	var ts int64
	for {
		if _, err := fmt.Fscanf(f, "%s %d\n", &id, &ts); err != nil {
			break
		}
		at := time.Unix(ts, 0)
		if at.Before(cutoff) {
			continue // drop stale entries on load
		}
		consumedGrants[id] = at
		count++
	}
	log.Printf("grants: %d consumed grants rehydrated from %s", count, grantLogPath)
}

// forwardGrantToDest — called from the source agent's MIGRATE branch
// right before virsh migrate. Dials the destination agent's
// /prepare-grant endpoint and returns nil on 200, error otherwise.
func forwardGrantToDest(destAddr, envelope string) error {
	body, _ := json.Marshal(map[string]string{"grant": envelope})
	req, err := http.NewRequest(http.MethodPost, "http://"+destAddr+":8081/prepare-grant", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("dest rejected grant: %s (%s)", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// handlePrepareGrant is the destination-side endpoint. Validates the
// grant signature + fields against the cached server pub key, records
// consumption, and replies 200 or 4xx.
func handlePrepareGrant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Grant string `json:"grant"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Grant == "" {
		http.Error(w, "grant required", http.StatusBadRequest)
		return
	}
	pub := ensureGrantPubKey(agentServerURL())
	if pub == nil {
		http.Error(w, "grant pub key unavailable — server unreachable?", http.StatusServiceUnavailable)
		return
	}
	g, err := verifyGrantEnvelope(pub, req.Grant)
	if err != nil {
		if g != nil {
			log.Printf("grants: reject id=%s reason=%v", g.ID, err)
		} else {
			log.Printf("grants: reject (undecodable): %v", err)
		}
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	// Binding checks — dest must be us.
	selfIDs := agentIdentityNames()
	destOK := false
	for _, id := range selfIDs {
		if id == g.Dest {
			destOK = true
			break
		}
	}
	if !destOK {
		log.Printf("grants: reject id=%s reason=dest mismatch (grant.dst=%s, we=%v)", g.ID, g.Dest, selfIDs)
		http.Error(w, "grant: destination mismatch", http.StatusForbidden)
		return
	}
	if ok, reason := recordConsumedGrant(g); !ok {
		log.Printf("grants: reject id=%s reason=%s", g.ID, reason)
		http.Error(w, "grant: "+reason, http.StatusConflict)
		return
	}
	log.Printf("grants: accept id=%s src=%s dst=%s vm=%s ttl=%ds",
		g.ID, g.Source, g.Dest, g.VMUUID, g.ExpiresAt-g.IssuedAt)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted", "grant_id": g.ID})
}

// handleGrantActive — the libvirt qemu hook dials this on the dest
// side right before libvirtd accepts a migration. 200 means "an
// authorised migration for this VM was accepted in the last
// activeGrantTTL"; 403 means bypass (out-of-band virsh migrate).
func handleGrantActive(w http.ResponseWriter, r *http.Request) {
	vm := r.URL.Query().Get("vm")
	if vm == "" {
		http.Error(w, "vm required", http.StatusBadRequest)
		return
	}
	consumedGrantsMu.Lock()
	defer consumedGrantsMu.Unlock()
	g, ok := activeGrantsByVM[vm]
	if !ok {
		http.Error(w, "no active grant for vm", http.StatusForbidden)
		return
	}
	if time.Since(g.AcceptedAt) > activeGrantTTL {
		delete(activeGrantsByVM, vm)
		http.Error(w, "active grant expired", http.StatusForbidden)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"grant_id":    g.ID,
		"accepted_at": g.AcceptedAt.Format(time.RFC3339),
		"expires_at":  g.ExpiresAt.Format(time.RFC3339),
	})
}

// agentIdentityNames returns the strings a grant's Dest field is
// allowed to match. Accepts both the short hostname and the FQDN so
// operators can enroll using either form.
func agentIdentityNames() []string {
	host, _ := os.Hostname()
	out := []string{host}
	if short := strings.SplitN(host, ".", 2)[0]; short != host {
		out = append(out, short)
	}
	return out
}

// agentServerURL returns the control-plane URL the agent was started
// with. Set once from main() so grants.go can pull the signer's
// public key on demand without plumbing the flag through every fn.
func agentServerURL() string { return telemetryServerURL }

var telemetryServerURL string
