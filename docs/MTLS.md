# Hypervisor mTLS — Secure Migration Channel

_Status: shipped in v5.10.0 → v5.10.8. Signed-grant authorization layer
lands in v5.11._

This document covers what the cluster CA does, how enrollment works,
what changes on each hypervisor after enrollment, and how migrations
flow over the encrypted channel. If you're operating a kvm-center
fleet and want to understand or troubleshoot the PKI, start here.

---

## 1. Why mTLS

Before v5.10, cross-host migrations rode `qemu+ssh://` over a shared
`kvm-migrator` ssh keypair copied to every hypervisor. Two weak
points:

1. **Static credential.** Anyone with the keypair file can initiate a
   migration against any node. Revocation means rotating the key on
   every host simultaneously.
2. **Coarse authorization.** The ssh user is root-equivalent on
   libvirt; there's no per-migration authorization gate.

v5.10's mTLS channel replaces the credential with per-host identity
certificates signed by a cluster CA. v5.11 will layer signed, single-
use migration grants on top so every migration is explicitly
authorized by the control plane.

---

## 2. Components

```
                 ┌─────────────────────────┐
                 │  kvm-center server      │
                 │                         │
                 │  Cluster CA (root)      │──┐  ECDSA P-256, 10-year
                 │  /var/lib/kvm-center/ca │  │  Auto-generated on
                 └──────────┬──────────────┘  │  first boot
                            │                 │
                            │ signs           │
                            ▼                 │
           ┌────────────────────────────┐     │
           │ Hypervisor leaf certs      │◄────┘  1-year TTL, CN=host ID,
           │ CN=hvnode0X.example.lan    │        SANs include FQDN + IP,
           │ Both serverAuth+clientAuth │        issued via one-time
           └────────────┬───────────────┘        enrollment tokens
                        │
                        ▼
           ┌────────────────────────────┐
           │ libvirtd-tls.socket :16514 │        Socket-activated by
           │ Runs on each hypervisor.   │        systemd; mutual TLS
           │ Validates peer cert chain  │        against the cluster CA.
           │ against the cluster CA.    │
           └────────────────────────────┘
```

- **Cluster CA** — the kvm-center server owns a self-signed ECDSA
  P-256 root kept at `/var/lib/kvm-center/ca/{ca.crt,ca.key}`. Ten-
  year TTL. Path is configurable via `KVM_CENTER_CA_DIR` or the
  `--ca-dir` flag; in Docker it's backed by the `kvm-center-data`
  volume so the key survives image rebuilds.

- **Hypervisor leaf cert** — one per hypervisor, CN = host ID
  (typically the FQDN, e.g. `hvnode01.example.lan`). SAN list
  includes the host ID, the FQDN derived from
  `KVM_CENTER_CLUSTER_DOMAIN`, and the host's IP from the Host
  record. Both `serverAuth` and `clientAuth` extended key usages are
  set — libvirtd uses the same cert as both endpoints in a
  bidirectional mutual-TLS handshake. One-year TTL.

- **Enrollment token** — one-time, ten-minute TTL, minted by an admin
  with the `tls.manage` permission. Bound to a specific host ID. The
  `/api/ca/sign` endpoint validates the token, consumes it, signs the
  attached CSR, and records issuance in the `hypervisor_certs`
  collection.

- **`libvirtd-tls.socket`** — the systemd socket unit libvirt ships.
  Listens on `:16514`. The install script enables this and disables
  `libvirtd-tcp.socket` so plain-text TCP never answers.

---

## 3. Enrollment

### 3.1 Operator mints a token (UI)

Settings → TLS → Hypervisor PKI → enter the host ID → **Issue
enrollment token**. Copy the `enroll_…` string — it's shown once and
expires in ten minutes.

The host ID you type here becomes the cert CN. We recommend the FQDN
(`hvnode01.example.lan`) so libvirt's hostname check against
`qemu+tls://<fqdn>/system` succeeds without relying on SANs alone.

Audit: `ca.enroll` is recorded with the actor + target host.

### 3.2 Agent exchanges a CSR for a leaf cert

On the target hypervisor run the installer with the token:

```sh
curl -fsSL http://REDACTED-HOST:8097/install-agent.sh | bash -s -- \
  --server http://REDACTED-HOST:8090 \
  --host-id hvnode01.example.lan \
  --enroll-token enroll_…
```

The script will, atomically:

1. Generate an ECDSA P-256 key under `/etc/kvm-center/tls/agent.key`
   and a CSR with `CN=<host-id>`.
2. `POST /api/ca/sign` to the kvm-center server with the token + CSR.
3. Write the returned leaf cert to `/etc/kvm-center/tls/agent.crt`
   and the CA bundle to `/etc/kvm-center/tls/ca.crt`.
4. Copy those into the paths libvirtd reads:

   ```
   /etc/pki/CA/cacert.pem                       ← CA bundle
   /etc/pki/libvirt/servercert.pem              ← leaf (server auth)
   /etc/pki/libvirt/clientcert.pem              ← leaf (client auth)
   /etc/pki/libvirt/private/serverkey.pem       ← private key
   /etc/pki/libvirt/private/clientkey.pem       ← private key
   ```

5. Flip `listen_tls=1`, `listen_tcp=0` in `/etc/libvirt/libvirtd.conf`.
6. `systemctl enable --now libvirtd-tls.socket`, disable
   `libvirtd-tcp.socket`, restart `libvirtd`.
7. Open firewalld for `16514/tcp` (TLS control) and `49152-49215/tcp`
   (QEMU migration data).

Audit: `ca.sign` records the issuance with the issued serial,
fingerprint, and expiry. The entry is owned by the admin who minted
the enrollment token.

### 3.3 Verification

From any enrolled hypervisor:

```sh
virsh -c qemu+tls://hvnode02.example.lan/system list --all
```

Should return the peer's VMs. A TLS verification failure surfaces as
`authentication failed: Failed to verify peer's certificate` — usually
means the CA isn't trusted on one side or the CN / SAN list doesn't
cover the hostname you dialed.

The UI shows an **mTLS** pill on the host detail page for every host
with an active cluster cert.

---

## 4. Migration flow

When the operator clicks migrate in the UI (or via `kvmctl vm
migrate`), the server does the following:

```
           1. /api/command
              action=MIGRATE
              host_id=<source>
              params.destination=<dest IP>
              ▼
  ┌──────────────────────────────────────────┐
  │ server/handlers.go handleCommand         │
  │                                          │
  │  if source.mTLSReady && dest.mTLSReady:  │
  │      params.migration_transport = "tls"  │
  │  else:                                   │
  │      params.migration_transport = "ssh"  │
  │                                          │
  │  POST http://<source-ip>:8081/command    │
  └─────────────────────┬────────────────────┘
                        ▼
           2. Source agent receives command
              (agent/commander.go MIGRATE branch)
              ▼
  ┌──────────────────────────────────────────┐
  │ migrationURI() picks the scheme:         │
  │   "tls" → qemu+tls://<dest>/system       │
  │   "ssh" → qemu+ssh://kvm-migrator@… legacy│
  │                                          │
  │ virsh migrate --live --persistent        │
  │               --undefinesource --verbose │
  │               <vm> <uri>                 │
  └─────────────────────┬────────────────────┘
                        ▼
           3. Source libvirtd opens qemu+tls
              to dest :16514, handshakes with
              mutual cert auth, then streams
              memory + disk over the auth'd
              channel on 49152-49215.
                        ▼
           4. Dest libvirtd starts the VM
              using the transferred state.
              --undefinesource removes the
              source domain definition on
              completion.
```

Key properties:

- **Both sides authenticate each other.** The source presents its
  client cert to the dest's `servercert`; the dest presents its
  server cert to the source's `clientcert`. Both chain to the cluster
  CA.
- **Revocation is immediate for new migrations.** Once an admin marks
  a host's cert revoked in the UI, subsequent migration attempts
  involving that host fall back to the ssh legacy path (if still
  available) or fail outright in v5.11+.
- **Fallback is automatic.** If either side isn't enrolled,
  `migration_transport` is set to `ssh` and libvirt dials
  `qemu+ssh://kvm-migrator@<dest>/system` as before. A mixed fleet
  during rollout keeps working.
- **Offline migrations** (scp + `virsh define`) are unaffected by
  v5.10 — they don't ride a `qemu+<scheme>://` URI at all. A future
  patch will tunnel the scp leg over the same TLS channel.

The Migrations table has a **Transport** column that tags each record
as **mTLS** (green lock pill) or **SSH**.

---

## 5. Network requirements

Each hypervisor must accept inbound:

| Port          | Proto | Purpose                             |
|---------------|-------|-------------------------------------|
| 8081/tcp      | tcp   | kvm-center agent (commands)         |
| 16514/tcp     | tcp   | libvirt TLS control channel         |
| 49152-49215/tcp | tcp | QEMU migration data range           |

The `install-agent.sh --enroll-token` path opens these via firewalld
automatically. iptables / nftables-only deployments need equivalent
rules.

---

## 6. Cert rotation

Leaf certs have a 1-year TTL. To rotate:

1. Settings → TLS → Hypervisor PKI → click **Revoke** next to the old
   cert. Confirm with a reason.
2. Mint a fresh enrollment token for the same host ID.
3. Re-run `install-agent.sh --enroll-token <new-token>` on the host.

Rotation is live — migrations in flight keep their existing TLS
session; only new sessions pick up the new cert. The old cert's
`RevokedAt` is persisted so the audit trail shows when and by whom.

CA rotation is not yet automated (v5.12 roadmap). Manual steps:

1. `kvm-center-server` flag `--ca-dir /path/to/new-ca`, drop fresh
   cert/key into place, restart server.
2. Revoke all existing hypervisor certs.
3. Re-enroll every hypervisor. The old trust root is discarded once
   the last leaf cert is rotated.

---

## 7. Files on disk reference

**On the server:**

```
/var/lib/kvm-center/ca/ca.crt       — cluster CA certificate (public)
/var/lib/kvm-center/ca/ca.key       — cluster CA private key (0600)
```

Persisted across restarts. Mongo stores per-hypervisor issuance
metadata in the `hypervisor_certs` collection (keyed by host ID, with
serial + SHA-256 fingerprint + expiry + revocation state).

**On each hypervisor:**

```
/etc/kvm-center/tls/agent.crt            — hypervisor leaf cert
/etc/kvm-center/tls/agent.key            — matching private key (0600)
/etc/kvm-center/tls/ca.crt               — cluster CA bundle
/etc/pki/CA/cacert.pem                   — libvirt's trust anchor
/etc/pki/libvirt/servercert.pem          — leaf (server auth)
/etc/pki/libvirt/clientcert.pem          — leaf (client auth)
/etc/pki/libvirt/private/serverkey.pem   — private key (0600)
/etc/pki/libvirt/private/clientkey.pem   — private key (0600)
/etc/libvirt/libvirtd.conf               — listen_tls=1, listen_tcp=0
```

---

## 8. Troubleshooting

| Symptom                                                       | Cause / Fix                                                                                  |
|---------------------------------------------------------------|----------------------------------------------------------------------------------------------|
| `enrollment token invalid or expired`                         | Token is one-time + 10 min TTL. Mint a fresh one.                                            |
| `Certificate [session] owner does not match the hostname`     | SAN list doesn't cover the hostname you connected with. Use FQDN matching the cert CN, or re-enroll with the IP/hostname in the SAN list. |
| `No route to host` on `:16514`                                | Destination firewall blocks the TLS control port. Open 16514/tcp.                            |
| `Failed to connect to '<host>:49153'`                         | QEMU data port blocked. Open 49152-49215/tcp.                                                |
| `authentication failed: Failed to verify peer's certificate`  | CA bundle mismatch. Both sides must trust the same cluster CA — re-run the installer.        |
| Host has cert but the UI doesn't show the mTLS pill           | `mtls_ready` derives from a valid non-revoked cert for the host ID. Check the Host row's ID matches the cert's CN. |
| Live migration leaves a "ghost" domain on source              | Pre-v5.10.4 bug — missing `--undefinesource`. Upgrade + retry.                               |

Server logs are the fastest signal — every `ca.*` audit event is
logged and persisted.

---

## 9. What's next (v5.11)

v5.11 layers per-migration authorization on top of this channel.
Before every migration, the source agent will call
`POST /api/migrations/grant` and receive an Ed25519-signed,
single-use, TTL-bounded token bound to `{src, dst, vm}`. The
destination agent validates the signature + binding before letting
libvirtd proceed (enforced via a `/etc/libvirt/hooks/qemu` hook so an
operator running `virsh migrate` out of band cannot bypass the
kvm-center control plane).

See `docs/ARCHITECTURE.md` for the full trust model.
