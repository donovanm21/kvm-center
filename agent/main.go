package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	serverURL := flag.String("server", envOrDefault("KVM_CENTER_SERVER", "http://10.10.100.8:8090"), "control plane URL")
	cmdPort := flag.Int("port", intEnvOrDefault("KVM_CENTER_AGENT_PORT", 8081), "command server port")
	interval := flag.Duration("interval", 30*time.Second, "telemetry interval")
	flag.Parse()

	cmdAddr := fmt.Sprintf(":%d", *cmdPort)

	// Start the command server in the background.
	go StartCommandServer(cmdAddr)

	// Give the command server a moment to bind.
	time.Sleep(100 * time.Millisecond)

	log.Printf("kvm-center agent  server=%s  interval=%s  cmdPort=%d",
		*serverURL, *interval, *cmdPort)

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

func sendTelemetry(serverURL string, agentPort int) {
	payload := CollectTelemetry(agentPort)

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("telemetry: marshal error: %v", err)
		return
	}

	url := serverURL + "/api/telemetry"
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("telemetry: send failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("telemetry: server returned %d", resp.StatusCode)
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
