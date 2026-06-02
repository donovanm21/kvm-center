package main

// Self-update (v5.27.1). The control plane POSTs /self-update when
// an operator clicks "Update agent" on Host detail. We download the
// latest arch-specific binary from the control plane's /downloads/,
// write it atomically over our own executable, and exit — systemd
// restarts us with the new binary.
//
// Why in-place: the agent is already running as a systemd-managed
// long-lived process. Replacing the binary on disk + exiting is
// safer than forking + exec'ing ourselves, which would orphan open
// libvirt connections mid-reconnect.

import (
	"crypto/ed25519"
	"crypto/sha256"
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
	"runtime"
	"time"
)

// agentBinaryManifest is the signed manifest the server publishes next to
// each agent binary at /downloads/kvm-center-agent-<arch>.sig (C2). The
// signature is the server's Ed25519 grant key over the raw SHA-256 digest;
// the agent already trusts that key for migration grants.
type agentBinaryManifest struct {
	SHA256 string `json:"sha256"`        // hex of the binary's SHA-256
	Sig    string `json:"sig"`           // base64 Ed25519 signature over the digest
	Alg    string `json:"alg,omitempty"` // "ed25519"
	KeyID  string `json:"keyid,omitempty"`
}

// checkBinarySignature verifies the manifest against the downloaded binary's
// digest using the current grant key, falling back to the previous key
// inside the rotation grace window. Returns nil only on a valid signature.
func checkBinarySignature(cur, prev ed25519.PublicKey, prevUntil time.Time, digest []byte, m agentBinaryManifest) error {
	if hex.EncodeToString(digest) != m.SHA256 {
		return fmt.Errorf("sha256 mismatch (manifest=%s actual=%s)", m.SHA256, hex.EncodeToString(digest))
	}
	sig, err := base64.StdEncoding.DecodeString(m.Sig)
	if err != nil {
		return fmt.Errorf("signature decode: %w", err)
	}
	if len(cur) == ed25519.PublicKeySize && ed25519.Verify(cur, digest, sig) {
		return nil
	}
	if len(prev) == ed25519.PublicKeySize && time.Now().Before(prevUntil) && ed25519.Verify(prev, digest, sig) {
		return nil
	}
	return errors.New("binary signature does not match the server's grant key")
}

type selfUpdateResponse struct {
	Success        bool   `json:"success"`
	Message        string `json:"message"`
	OldBytes       int64  `json:"old_bytes,omitempty"`
	NewBytes       int64  `json:"new_bytes,omitempty"`
	BinaryPath     string `json:"binary_path,omitempty"`
	DownloadedFrom string `json:"downloaded_from,omitempty"`
}

// handleSelfUpdate is POST-only. The route is gated by command-auth (C1) so
// only the control plane can trigger it, and the downloaded binary must carry
// a valid Ed25519 signature from the server's grant key (C2) before it
// replaces the running binary. The download server is pinned to the agent's
// configured server URL — a caller-supplied override is rejected, since an
// attacker-controlled URL would otherwise be a remote-code-execution path.
func handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if selfUpdateBinaryPath == "" {
		http.Error(w, "agent self-update not configured (SetSelfUpdateConfig was not called at boot)", http.StatusPreconditionFailed)
		return
	}
	// v5.30.0: refuse while a migration is in flight. Killing the agent
	// mid-scp orphans the transfer + strands the server record because
	// no terminal signal ever arrives. The v5.29.0 deploy hit this
	// exact case on VM2; the VM1 migration the user retried later had
	// the agent self-update mid-flight too. Operator can override by
	// stopping the migration first.
	if r.URL.Query().Get("force") != "1" {
		if status := getMigStatus(); status.Active {
			http.Error(w, fmt.Sprintf(
				"refusing self-update: a migration is in flight (vm=%s, phase=%s, %d%%). Cancel it first or retry with ?force=1.",
				status.VMName, status.Phase, status.Progress),
				http.StatusConflict)
			return
		}
	}
	// Pin to the configured server. Reject any caller-supplied override —
	// downloading + executing a binary from an attacker-named URL would be
	// straight RCE.
	server := selfUpdateServerURL
	var body struct {
		Server string `json:"server"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Server != "" && body.Server != server {
		writeSelfUpdateErr(w, "server URL override is not allowed; the agent only updates from its configured control plane")
		return
	}
	if server == "" {
		http.Error(w, "server URL not configured", http.StatusPreconditionFailed)
		return
	}

	arch := runtime.GOARCH // "amd64" | "arm64"
	url := fmt.Sprintf("%s/downloads/kvm-center-agent-%s", server, arch)

	log.Printf("self-update: downloading %s", url)
	// Tolerate the self-signed control-plane cert — same rationale as
	// install-agent.sh uses `curl -k`. Use the existing httpClient
	// which already has InsecureSkipVerify when the server URL is
	// https (see agent/main.go).
	resp, err := httpClient.Get(url)
	if err != nil {
		writeSelfUpdateErr(w, "download failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeSelfUpdateErr(w, fmt.Sprintf("download: HTTP %d", resp.StatusCode))
		return
	}

	// Stream to a tmp file in the same directory so the atomic rename
	// works (os.Rename across filesystems is not atomic).
	dir := filepath.Dir(selfUpdateBinaryPath)
	tmp, err := os.CreateTemp(dir, ".kvm-center-agent-new-*")
	if err != nil {
		writeSelfUpdateErr(w, "tmp create: "+err.Error())
		return
	}
	hasher := sha256.New()
	n, err := io.Copy(tmp, io.TeeReader(resp.Body, hasher))
	tmp.Close()
	if err != nil {
		_ = os.Remove(tmp.Name())
		writeSelfUpdateErr(w, "download write: "+err.Error())
		return
	}
	// Basic sanity — agent binary is ~14 MB, refuse anything tiny.
	if n < 1_000_000 {
		_ = os.Remove(tmp.Name())
		writeSelfUpdateErr(w, fmt.Sprintf("downloaded binary too small (%d bytes) — aborting", n))
		return
	}
	// C2: verify the binary's signature against the server's grant key
	// BEFORE making it executable or swapping it in. Fail closed — never
	// run an unsigned or tampered binary.
	digest := hasher.Sum(nil)
	if err := verifyDownloadedBinary(server, arch, digest); err != nil {
		_ = os.Remove(tmp.Name())
		writeSelfUpdateErr(w, "signature verification failed: "+err.Error())
		return
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		_ = os.Remove(tmp.Name())
		writeSelfUpdateErr(w, "chmod: "+err.Error())
		return
	}

	var oldBytes int64
	if st, err := os.Stat(selfUpdateBinaryPath); err == nil {
		oldBytes = st.Size()
	}

	if err := os.Rename(tmp.Name(), selfUpdateBinaryPath); err != nil {
		_ = os.Remove(tmp.Name())
		writeSelfUpdateErr(w, "rename: "+err.Error())
		return
	}
	log.Printf("self-update: replaced %s (%d → %d bytes)", selfUpdateBinaryPath, oldBytes, n)

	_ = json.NewEncoder(w).Encode(selfUpdateResponse{
		Success:        true,
		Message:        fmt.Sprintf("agent binary replaced (%d bytes); restarting in 1 s", n),
		OldBytes:       oldBytes,
		NewBytes:       n,
		BinaryPath:     selfUpdateBinaryPath,
		DownloadedFrom: url,
	})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	// Give the HTTP response a moment to flush, then exit so systemd
	// restarts us with the new binary.
	go func() {
		time.Sleep(1 * time.Second)
		log.Println("self-update: exiting for systemd restart")
		os.Exit(0)
	}()
}

// verifyDownloadedBinary fetches the binary's signed manifest from the server
// and verifies the Ed25519 signature over digest against the grant key the
// agent already trusts for migration grants. Fails closed if the manifest is
// missing, malformed, or the signature doesn't match.
func verifyDownloadedBinary(server, arch string, digest []byte) error {
	sigURL := fmt.Sprintf("%s/downloads/kvm-center-agent-%s.sig", server, arch)
	resp, err := httpClient.Get(sigURL)
	if err != nil {
		return fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("manifest HTTP %d (server too old to sign agent binaries?)", resp.StatusCode)
	}
	var m agentBinaryManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&m); err != nil {
		return fmt.Errorf("manifest decode: %w", err)
	}
	cur, prev, prevUntil := grantKeysForVerify(server)
	if len(cur) == 0 && len(prev) == 0 {
		return errors.New("no grant public key available to verify the binary")
	}
	return checkBinarySignature(cur, prev, prevUntil, digest, m)
}

func writeSelfUpdateErr(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(selfUpdateResponse{Success: false, Message: msg})
}
