package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Version is injected at build time via -ldflags.
var Version = "dev"

func main() {
	serverURL := flag.String("server", envOrDefault("KVM_CENTER_SERVER", "http://10.10.100.8:8090"), "control plane URL")
	cmdPort := flag.Int("port", intEnvOrDefault("KVM_CENTER_AGENT_PORT", 8081), "command server port")
	interval := flag.Duration("interval", 30*time.Second, "telemetry interval")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		log.Printf("kvm-center-agent %s", Version)
		os.Exit(0)
	}
	log.Printf("kvm-center-agent starting, version=%s", Version)

	cmdAddr := fmt.Sprintf(":%d", *cmdPort)

	// Start the command server in the background.
	go StartCommandServer(cmdAddr)

	// Give the command server a moment to bind.
	time.Sleep(100 * time.Millisecond)

	log.Printf("kvm-center agent  server=%s  interval=%s  cmdPort=%d",
		*serverURL, *interval, *cmdPort)

	// Expose the server URL to other agent packages (grants.go needs it
	// to fetch the signer's public key on demand).
	telemetryServerURL = *serverURL

	// Pick the outbound HTTP client based on the server URL scheme;
	// logs the "TLS skip-verify" warning when we flip to https.
	initHTTPClient(*serverURL)

	// Rehydrate the console-token secret from disk so a restart doesn't
	// reject console tokens until the next telemetry tick. Host id set
	// here (matches what CollectTelemetry reports) scopes the token
	// signature to this agent.
	LoadCachedConsoleSecret()
	// Must match whatever collector.go's hostname() reports as HostID —
	// the server signs tokens against that exact string.
	SetHostIDForToken(hostname())

	// Collect and send telemetry immediately, then on interval.
	sendTelemetry(*serverURL, *cmdPort)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			sendTelemetry(*serverURL, *cmdPort)
		case s := <-sig:
			log.Printf("agent: received %s, shutting down", s)
			return
		}
	}
}

// httpClient is the shared client used for all outbound posts to the
// kvm-center control plane. When the server URL is https:// we set
// InsecureSkipVerify so agents can reach the server's auto-generated
// self-signed cert without per-agent CA provisioning — necessary for
// homelab convenience but logged at startup (and on every send) as
// INSECURE so the operator can upgrade to a trusted cert deliberately.
var httpClient = &http.Client{}
var tlsInsecureWarningShown bool

func initHTTPClient(serverURL string) {
	if !strings.HasPrefix(serverURL, "https://") {
		httpClient = &http.Client{Timeout: 10 * time.Second}
		return
	}
	httpClient = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // homelab trust model: see comment above
		},
	}
	if !tlsInsecureWarningShown {
		log.Printf("⚠ kvm-center-agent: TLS verification DISABLED for %s — trusting any certificate. "+
			"Acceptable for a homelab where the server uses the auto-generated self-signed cert; "+
			"install a trusted cert via Settings → TLS and swap the URL when you're ready.", serverURL)
		tlsInsecureWarningShown = true
	}
}

func sendTelemetry(serverURL string, agentPort int) {
	payload := CollectTelemetry(agentPort)

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("telemetry: marshal error: %v", err)
		return
	}

	url := serverURL + "/api/telemetry"
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("telemetry: send failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("telemetry: server returned %d", resp.StatusCode)
		return
	}
	// Server ships the console-token secret on every reply so a key
	// rotation propagates within one telemetry tick. Safe to ignore
	// decode errors — we just keep the cached secret unchanged.
	if body, err := io.ReadAll(resp.Body); err == nil {
		ParseTelemetryReply(body)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnvOrDefault(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}
