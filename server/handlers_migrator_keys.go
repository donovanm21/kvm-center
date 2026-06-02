package main

// Fleet-wide kvm-migrator authorized_keys distribution (v5.55.4).
//
// Each agent ships its `kvm-migrator` SSH pubkey in every telemetry
// tick (TelemetryPayload.MigratorPubkey). This endpoint aggregates
// the pubkeys of every currently-active host and returns them as a
// plaintext authorized_keys blob, one OpenSSH-format key per line,
// terminated by newlines.
//
// The agent's reconciler GETs this every hour and overwrites
// /var/lib/kvm-migrator/.ssh/authorized_keys with the response. This
// means:
//   - A freshly enrolled host's pubkey lands in every other host's
//     authorized_keys within one tick → SSH migrations work without
//     manual key meshing.
//   - A retired host's pubkey is excluded (it's in the removedHosts
//     deny-list) so its key evaporates from the fleet on the next
//     reconcile → no stale trust anchors lingering.

import (
	"net/http"
	"sort"
	"strings"
)

// handleAgentMigratorAuthorizedKeys returns the fleet's kvm-migrator
// authorized_keys content as plaintext. Public endpoint (the pubkeys
// are public material by design — they only authorise the holder of
// the matching private key, which lives only on the agents).
func handleAgentMigratorAuthorizedKeys(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")

		hosts := store.GetHosts()
		// Dedupe + sort so the agent sees stable output (some hosts
		// share the same kvm-migrator key from cloned provisioning).
		seen := make(map[string]struct{}, len(hosts))
		lines := make([]string, 0, len(hosts))
		for _, h := range hosts {
			if h == nil || h.MigratorPubkey == "" {
				continue
			}
			if store.IsHostRemoved(h.ID) {
				continue
			}
			key := strings.TrimSpace(h.MigratorPubkey)
			if key == "" {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			lines = append(lines, key)
		}
		sort.Strings(lines)

		var sb strings.Builder
		for _, l := range lines {
			sb.WriteString(l)
			sb.WriteByte('\n')
		}
		_, _ = w.Write([]byte(sb.String()))
	}
}
