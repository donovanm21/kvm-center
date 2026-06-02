package main

// Fleet-wide kvm-migrator authorized_keys sync (v5.55.4).
//
// Each agent reads its own /var/lib/kvm-migrator/.ssh/id_ed25519.pub
// at startup and includes it in every telemetry payload. The server
// aggregates the pubkeys of all active hosts and serves them back at
// /api/agents/migrator-authorized-keys. This sync goroutine pulls
// that blob hourly and overwrites the local authorized_keys, so:
//
//   - A new host's pubkey lands in the fleet on the first telemetry
//     tick and is mirrored into every other host's authorized_keys
//     within the hour.
//   - A retired host is excluded from the server's response and so
//     evaporates from every authorized_keys file on the next tick.
//
// The file is fully managed — we don't merge with operator additions.
// Operators wanting to grant additional access should use a separate
// file (sshd `AuthorizedKeysFile` accepts a list) or run scripts via
// root, not the migrator account.

import (
	"io"
	"log"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"
)

const (
	migratorPubkeyPath         = "/var/lib/kvm-migrator/.ssh/id_ed25519.pub"
	migratorAuthorizedKeysPath = "/var/lib/kvm-migrator/.ssh/authorized_keys"
	migratorKeysRefreshTick    = 1 * time.Hour
)

// cachedMigratorPubkey holds the single-line OpenSSH pubkey for the
// kvm-migrator user on this host. Populated once at startup; included
// in every telemetry payload. Empty when the file is missing (a host
// where the migrator user hasn't been provisioned yet) — the server
// treats empty as "skip this host" rather than wiping any prior value.
var cachedMigratorPubkey string

// cachedSSHHostKeys (v5.56.1) holds the host's SSH server public
// keys (one OpenSSH-format line per key type). Populated once at
// startup; the server aggregates them into the fleet-wide
// known_hosts blob so a reinstalled host's rotated host key
// propagates without operator intervention.
var cachedSSHHostKeys []string

// loadMigratorPubkey reads the agent's kvm-migrator pubkey from disk
// into cachedMigratorPubkey. Returns the loaded value (or empty on
// failure). Tolerant of a missing file — the agent must still run on
// hosts that haven't been fully provisioned for SSH migration.
func loadMigratorPubkey() string {
	data, err := os.ReadFile(migratorPubkeyPath)
	if err != nil {
		log.Printf("migrator-keys: pubkey not readable at %s: %v (SSH migrations to this host will fail until fixed)", migratorPubkeyPath, err)
		cachedMigratorPubkey = ""
		return ""
	}
	cachedMigratorPubkey = strings.TrimSpace(string(data))
	return cachedMigratorPubkey
}

// loadSSHHostKeys (v5.56.1) reads each /etc/ssh/ssh_host_*_key.pub
// file into cachedSSHHostKeys. Missing files are silently skipped
// (a server may not have every key type configured). Caller fires
// this once at startup; subsequent reads use the cached value.
func loadSSHHostKeys() []string {
	cachedSSHHostKeys = cachedSSHHostKeys[:0]
	candidates := []string{
		"/etc/ssh/ssh_host_ed25519_key.pub",
		"/etc/ssh/ssh_host_ecdsa_key.pub",
		"/etc/ssh/ssh_host_rsa_key.pub",
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		line := strings.TrimSpace(string(data))
		if line == "" {
			continue
		}
		cachedSSHHostKeys = append(cachedSSHHostKeys, line)
	}
	if len(cachedSSHHostKeys) == 0 {
		log.Printf("ssh-host-keys: no /etc/ssh/ssh_host_*_key.pub readable (SSH migrations to this host won't have fleet-known known_hosts entries until fixed)")
	}
	return cachedSSHHostKeys
}

// startMigratorKeysSync launches the periodic authorized_keys
// reconciliation loop. Failures are logged + tolerated; SSH migration
// keeps working off whatever the file already contains.
func startMigratorKeysSync(serverURL string) {
	if serverURL == "" {
		return
	}
	go func() {
		// 30 s warm-up so the first telemetry tick lands first; the
		// server can't include our pubkey in the response until we've
		// reported it.
		time.Sleep(30 * time.Second)
		for {
			if err := pullMigratorAuthorizedKeys(serverURL); err != nil {
				log.Printf("migrator-keys: refresh failed: %v", err)
			}
			time.Sleep(migratorKeysRefreshTick)
		}
	}()
}

func pullMigratorAuthorizedKeys(serverURL string) error {
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: agentServerTLSConfig()},
	}
	resp, err := client.Get(serverURL + "/api/agents/migrator-authorized-keys")
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
		// Server has no pubkeys yet (fresh cluster, this agent
		// hasn't reported its own pubkey yet). Skip rather than
		// truncate the existing authorized_keys.
		return nil
	}
	tmp := migratorAuthorizedKeysPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if u, err := user.Lookup("kvm-migrator"); err == nil {
		if uid, _ := strconv.Atoi(u.Uid); uid > 0 {
			if gid, _ := strconv.Atoi(u.Gid); gid > 0 {
				_ = os.Chown(tmp, uid, gid)
			}
		}
	}
	return os.Rename(tmp, migratorAuthorizedKeysPath)
}
