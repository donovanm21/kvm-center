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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type selfUpdateResponse struct {
	Success        bool   `json:"success"`
	Message        string `json:"message"`
	OldBytes       int64  `json:"old_bytes,omitempty"`
	NewBytes       int64  `json:"new_bytes,omitempty"`
	BinaryPath     string `json:"binary_path,omitempty"`
	DownloadedFrom string `json:"downloaded_from,omitempty"`
}

// handleSelfUpdate is POST-only. Optional body {server: "https://..."}
// overrides the configured server URL for the download. No auth
// today — the endpoint is only exposed on :8081 on the management
// VLAN. v5.28 will add the same HMAC-token gate the console RPC
// uses (token minted by the server right before the POST).
func handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if selfUpdateBinaryPath == "" {
		http.Error(w, "agent self-update not configured (SetSelfUpdateConfig was not called at boot)", http.StatusPreconditionFailed)
		return
	}
	server := selfUpdateServerURL
	var body struct {
		Server string `json:"server"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Server != "" {
		server = body.Server
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
	n, err := io.Copy(tmp, resp.Body)
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

func writeSelfUpdateErr(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(selfUpdateResponse{Success: false, Message: msg})
}
