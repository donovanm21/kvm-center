package main

import (
	"bytes"
	"context"
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
	serverURL := flag.String("server", envOrDefault("KVM_CENTER_SERVER", "https://kvm-center:8190"), "control plane URL (TLS-only since v5.32.0)")
	cmdPort := flag.Int("port", intEnvOrDefault("KVM_CENTER_AGENT_PORT", 8081), "command server port")
	interval := flag.Duration("interval", 15*time.Second, "telemetry interval")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		log.Printf("kvm-center-agent %s", Version)
		os.Exit(0)
	}
	log.Printf("kvm-center-agent starting, version=%s", Version)

	cmdAddr := fmt.Sprintf(":%d", *cmdPort)

	// Wire the /self-update handler with where to pull new binaries
	// from + where on disk to replace. Best-effort — os.Executable()
	// can fail on exotic FS setups; we just log and leave
	// self-update disabled in that case.
	if self, err := os.Executable(); err == nil {
		SetSelfUpdateConfig(*serverURL, self)
	} else {
		log.Printf("self-update disabled: os.Executable: %v", err)
	}

	// Start the command server in the background.
	go StartCommandServer(cmdAddr)

	// Give the command server a moment to bind.
	time.Sleep(100 * time.Millisecond)

	log.Printf("kvm-center agent  server=%s  interval=%s  cmdPort=%d",
		*serverURL, *interval, *cmdPort)

	// Expose the server URL to other agent packages (grants.go needs it
	// to fetch the signer's public key on demand).
	telemetryServerURL = *serverURL
	// Expose the command port so /refresh-telemetry can report it in
	// the on-demand telemetry payload. Tier S #1, v5.32.21.
	agentCommandPort = *cmdPort
	// Rehydrate consumed-grant replay log so a restart doesn't reset
	// single-use enforcement.
	LoadConsumedGrantsLog()

	// Pick the outbound HTTP client based on the server URL scheme;
	// logs the "TLS skip-verify" warning when we flip to https.
	initHTTPClient(*serverURL)

	// Rehydrate the console-token secret from disk so a restart doesn't
	// reject console tokens until the next telemetry tick. Host id set
	// here (matches what CollectTelemetry reports) scopes the token
	// signature to this agent.
	LoadCachedConsoleSecret()
	// Rehydrate the command-auth enforcement flag (C1) so a restarted agent
	// keeps enforcing signatures from boot rather than briefly accepting
	// unsigned commands until the first telemetry reply.
	LoadCachedCommandAuthActive()
	// Must match whatever collector.go's hostname() reports as HostID —
	// the server signs tokens against that exact string.
	SetHostIDForToken(hostname())

	// v5.28.0: open a persistent gRPC stream that pushes per-running-VM
	// CPU/mem samples every ~3 s so the SPA's inventory reflects live
	// numbers without waiting for the 15 s HTTP telemetry tick.
	liveCtx, liveCancel := context.WithCancel(context.Background())
	defer liveCancel()
	go startLiveMetricsStreamer(liveCtx, *serverURL)

	// v5.48 (PR-2a): persistent ops.db at
	// /var/lib/kvm-center/operations.db (override KVM_AGENT_OPS_DB).
	// Replaces the in-memory outcome cache so command outcomes survive
	// agent restarts and the boot reconciler (PR-2d) can resolve any
	// in-flight rows against libvirt reality. Failure to open is
	// non-fatal — the agent keeps running, but slow-action durability
	// degrades to the v5.32.17 in-memory cache.
	opsDBPath := envOrDefault("KVM_AGENT_OPS_DB", DefaultOpsDBPath)
	if store, err := OpenOperationsStore(opsDBPath); err != nil {
		log.Printf("ops.db: open %s failed: %v — falling back to in-memory cache only", opsDBPath, err)
	} else {
		ops = store
		log.Printf("ops.db: opened %s", opsDBPath)
		// PR-2d: resolve any rows the previous agent process left in
		// `running` state by querying libvirt reality. Runs early
		// during agent startup; the command server is already
		// accepting new requests by this point but new requests use
		// fresh request_ids, so there's no collision with the
		// in-flight set being reconciled.
		bootCtx, bootCancel := context.WithTimeout(liveCtx, 30*time.Second)
		reconcileBoot(bootCtx, store)
		bootCancel()
		go runOpsGC(liveCtx, store)
		go runOpsHeartbeat(liveCtx, store)
	}

	// v5.29.0: watch VM power-state transitions and fire an immediate
	// telemetry POST when anything flips, so the SPA reflects a VM
	// going running↔shutoff within ~2 s instead of up to 15 s.
	go startLifecycleWatcher(liveCtx, *serverURL, *cmdPort)
	// v5.46.0: refresh /var/lib/kvm-migrator/.ssh/known_hosts from
	// the operator-curated content on the server. Once at startup
	// (after a 30 s warm-up) and again every hour.
	startKnownHostsSync(*serverURL)
	// v5.55.4: read the local kvm-migrator pubkey once so every
	// telemetry payload can carry it, then launch the periodic
	// authorized_keys reconciler so a newly enrolled host gets
	// trusted across the fleet within the hour.
	loadMigratorPubkey()
	startMigratorKeysSync(*serverURL)
	// v5.56.1: read this host's SSH server pubkeys once so every
	// telemetry payload can carry them. The existing
	// startKnownHostsSync above already polls the server for the
	// reconciled fleet known_hosts blob each hour.
	loadSSHHostKeys()

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
// kvm-center control plane.
//
// Default trust model (homelab convenience): when the server URL is
// https:// we set InsecureSkipVerify so agents can reach the server's
// auto-generated self-signed cert without per-agent CA provisioning.
// Logged at startup as INSECURE.
//
// Production trust model (opt-in): when KVM_CENTER_AGENT_VERIFY_TLS=1
// the agent verifies the server cert against either the system trust
// store (default) or the PEM bundle at $KVM_CENTER_SERVER_CA. Use this
// in any deployment where the server has a real CA-signed cert (Let's
// Encrypt, internal corporate CA, etc.) — gives you man-in-the-middle
// protection on the telemetry channel.
var httpClient = &http.Client{}
var tlsInsecureWarningShown bool

func initHTTPClient(serverURL string) {
	if !strings.HasPrefix(serverURL, "https://") {
		httpClient = &http.Client{Timeout: 10 * time.Second}
		return
	}
	tlsCfg := agentServerTLSConfig()
	httpClient = &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
	if !tlsCfg.InsecureSkipVerify {
		if caPath := os.Getenv("KVM_CENTER_SERVER_CA"); caPath != "" {
			log.Printf("kvm-center-agent: TLS verification ENABLED for %s (CA: %s)", serverURL, caPath)
		} else {
			log.Printf("kvm-center-agent: TLS verification ENABLED for %s (system trust store)", serverURL)
		}
	}
	if tlsCfg.InsecureSkipVerify && !tlsInsecureWarningShown {
		log.Printf("⚠ kvm-center-agent: TLS verification DISABLED for %s — trusting any certificate. "+
			"Acceptable for a homelab where the server uses the auto-generated self-signed cert; "+
			"set KVM_CENTER_AGENT_VERIFY_TLS=1 (and KVM_CENTER_SERVER_CA=/path/to/ca.pem if needed) "+
			"to enable verification.", serverURL)
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
