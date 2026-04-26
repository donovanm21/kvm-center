package main

// SSH known_hosts sync (v5.46.0). Pulls the operator-curated
// known_hosts content from `/api/agents/ssh-known-hosts` and
// writes it to `/var/lib/kvm-migrator/.ssh/known_hosts` so the
// agent's SSH-transport migration paths can verify host keys when
// `Features.SSHStrictHostKeyChecking` is on.
//
// Runs once at startup (after the first telemetry post) and again
// every hour. No-op when the server returns an empty body (fresh
// install, operator hasn't curated yet).

import (
	"crypto/tls"
	"io"
	"log"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"time"
)

const (
	knownHostsPath        = "/var/lib/kvm-migrator/.ssh/known_hosts"
	knownHostsRefreshTick = 1 * time.Hour
)

// startKnownHostsSync launches the periodic refresh loop. Failures
// are logged + tolerated; the migration paths fall back to whatever
// known_hosts already exists.
func startKnownHostsSync(serverURL string) {
	if serverURL == "" {
		return
	}
	go func() {
		// First pull happens 30 s after start so the telemetry tick
		// gets a chance to register the agent first.
		time.Sleep(30 * time.Second)
		for {
			if err := pullKnownHosts(serverURL); err != nil {
				log.Printf("known-hosts: refresh failed: %v", err)
			}
			time.Sleep(knownHostsRefreshTick)
		}
	}()
}

func pullKnownHosts(serverURL string) error {
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
	}
	resp, err := client.Get(serverURL + "/api/agents/ssh-known-hosts")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil // operator hasn't curated yet
	}
	if err := os.WriteFile(knownHostsPath, body, 0o644); err != nil {
		return err
	}
	if u, err := user.Lookup("kvm-migrator"); err == nil {
		if uid, _ := strconv.Atoi(u.Uid); uid > 0 {
			if gid, _ := strconv.Atoi(u.Gid); gid > 0 {
				_ = os.Chown(knownHostsPath, uid, gid)
			}
		}
	}
	return nil
}
