---
title: Deployment
section: Getting started
order: 25
---

# Deployment

Two ways to run kvm-center, in order of how quickly you can be poking
at the SPA from a fresh machine:

| Path | Time to running | Best for |
|---|---|---|
| **[Docker / docker-compose](#docker)** | ~1 minute | trying it out, single-host deploys, dev setups |
| **[Bare-metal `install.sh`](#bare-metal)** | ~10 minutes | custom OS, ops shop with a config-management story |

Each path uses the same images / binaries — once you've installed,
the upgrade flow is "pull a new image / binary, restart" with no
data migration. Pick on convenience, not lock-in.

> A pre-built KVM appliance (qcow2) was published in v5.49.3 through
> v5.49.6 but was withdrawn for v5.50 while we stabilise the build
> pipeline. The Docker and bare-metal paths cover the same use cases
> in the meantime — bring a small VM, run docker-compose or
> install.sh, you're up.

## Docker

> ⚠ **The `thedon21/kvm-center` repo is currently private during
> development.** You'll need `docker login -u thedon21` first; the
> maintainer will share access on request. Will be flipped public in
> a future release.

### One-liner (no Mongo — JSON file persistence)

```sh
docker login -u thedon21
docker run -d --name kvm-center -p 8190:8443 \
  -v kvm-center-data:/var/lib/kvm-center \
  -e KVM_CENTER_PUBLIC_URL=https://localhost:8190 \
  thedon21/kvm-center:latest
```

Open `https://localhost:8190`, accept the self-signed cert, walk the
captive setup wizard. The `kvm-center-data` named volume holds your
state; back it up like any other Docker volume.

### docker-compose (with MongoDB — recommended)

`docker-compose.yml`:

```yaml
services:
  mongo:
    image: mongo:7
    restart: unless-stopped
    volumes: [mongo-data:/data/db]
    environment:
      MONGO_INITDB_ROOT_USERNAME: root
      MONGO_INITDB_ROOT_PASSWORD: ${MONGO_PASSWORD:?set MONGO_PASSWORD}

  server:
    image: thedon21/kvm-center:latest
    restart: unless-stopped
    depends_on: [mongo]
    ports: ["8190:8443"]
    environment:
      KVM_CENTER_MONGO_URL: mongodb://root:${MONGO_PASSWORD}@mongo:27017
      KVM_CENTER_PUBLIC_URL: https://localhost:8190
    volumes: [kvm-center-data:/var/lib/kvm-center]

volumes:
  mongo-data:
  kvm-center-data:
```

Bring it up:

```sh
docker login -u thedon21
MONGO_PASSWORD=$(openssl rand -base64 24) docker compose up -d
docker compose logs -f server
```

### Updating

```sh
docker compose pull && docker compose up -d
```

#### Auto-update with Watchtower

If you're tracking a rolling tag (`:latest`, `:5`, `:5.49`) and want
the host to pick up new pushes automatically, add Watchtower:

```yaml
services:
  watchtower:
    image: containrrr/watchtower
    restart: unless-stopped
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    command: --label-enable --cleanup --interval 3600
```

…and label your kvm-center service so Watchtower opts it in:

```yaml
  server:
    image: thedon21/kvm-center:5.49
    labels:
      com.centurylinklabs.watchtower.enable: "true"
```

Watchtower polls Docker Hub once an hour, pulls a newer manifest if
the rolling tag has moved, and restarts the container with the same
mounts/env. Pin `:vX.Y.Z` instead of a rolling tag if you don't want
automatic upgrades.

### Tag channels

| Tag | Tracks |
|---|---|
| `:latest` | last published minor / major release (rolling) |
| `:5.49` | last 5.49.x patch (stable channel) |
| `:5` | last 5.x release |
| `:vX.Y.Z` | exact release, immutable |

Pin `:vX.Y.Z` for production; pin `:5.49` if you want patch updates
without minor-version surprises.

### Verifying the image (cosign)

Every published image is signed. See
[`docs/security/README.md`](https://github.com/donovanm21/kvm-center/blob/main/docs/security/README.md)
for the verification flow:

```sh
cosign verify \
  --key https://raw.githubusercontent.com/donovanm21/kvm-center/main/docs/security/cosign.pub \
  thedon21/kvm-center:v5.49.0
```

## Bare metal

The original install path. Pulls the binary from your existing
kvm-center server's `/downloads/` portal:

```sh
curl -fsSLk https://<your-kvm-center-server>:8190/downloads/install.sh | sudo bash
```

The script auto-detects whether you're on a hypervisor (installs
the agent) or a guest VM (installs kvm-tools). Custom flags + the
full inventory are documented in [Getting started](getting-started).

## Bring-your-own infrastructure

Three integration paths matter most for slotting kvm-center into an
existing deployment without reaching for the bundled compose file:
external MongoDB, external TLS certificate, and a reverse-proxy
front door. Each is opt-in via env var.

### Use an existing MongoDB

Default is the `kvm-center-mongo` container in the bundled compose
recipe. To point at an external Mongo (replica set, TLS, hosted
Atlas, etc.) set:

| Variable | Value |
|---|---|
| `KVM_CENTER_MONGO_URL` | Full Mongo URI: `mongodb://user:pass@host:27017/?authSource=admin` or `mongodb+srv://user:pass@cluster.example.com/?retryWrites=true&w=majority` |
| `KVM_CENTER_MONGO_DB` | Database name (default: `kvm-center`) |

Notes:

- **Minimum Mongo version: 5.0**. v6/v7 work; v4.x is missing the
  TTL-on-zero index option used for command-log expiry.
- TLS to Mongo is honoured automatically when the URI scheme is
  `mongodb+srv://` or includes `tls=true`. Provide the CA bundle via
  the standard `MONGODB_TLS_CA_FILE` env if your Mongo is
  self-signed against a private CA.
- The user kvm-center connects as needs `readWrite` on its database.
  Initial bootstrap also creates indexes (`createIndex`), so the
  `dbAdmin` role is required on first boot only — drop it back to
  `readWrite` afterward if you prefer.
- First-boot migration from the JSON file backend to Mongo runs
  automatically when `KVM_CENTER_MONGO_URL` is set on a host that
  already has a `data.json`. The JSON file is then renamed
  `data.json.migrated-<timestamp>` so a re-run is idempotent.

### Bring your own TLS certificate

Self-signed by default. To use an operator-issued cert (Let's Encrypt
sidecar, internal PKI, public CA) set:

| Variable | Value |
|---|---|
| `KVM_CENTER_TLS_CERT` | PEM-encoded certificate chain (server cert + intermediates) |
| `KVM_CENTER_TLS_KEY` | PEM-encoded private key matching the cert |

Both must be set together — either flag alone is a config error.
Inside the Docker image, the most reliable approach is to bind-mount
the cert + key into the container and point the env vars at the
mounted paths:

```yaml
services:
  server:
    image: thedon21/kvm-center:latest
    volumes:
      - /etc/letsencrypt/live/kvm.example.com/fullchain.pem:/etc/kvm-center/server.crt:ro
      - /etc/letsencrypt/live/kvm.example.com/privkey.pem:/etc/kvm-center/server.key:ro
    environment:
      KVM_CENTER_TLS_CERT: /etc/kvm-center/server.crt
      KVM_CENTER_TLS_KEY:  /etc/kvm-center/server.key
```

Rotation: drop the new cert/key into the same paths and either
restart the container or reload via the SPA: **Settings → TLS →
Reload server certificate**. The reload watcher detects the file
mtime change within ~30 s and swaps the cert without dropping
in-flight connections.

The cluster CA used for signing hypervisor leaf certs is separate
and lives in the `kvm-center-data` volume (`/var/lib/kvm-center/ca/`).
Operator certs only replace the public-facing server leaf — the
cluster CA stays internal.

### Behind a reverse proxy

When kvm-center sits behind a reverse proxy (nginx, Traefik, Caddy,
HAProxy, k8s ingress), set `KVM_CENTER_TRUSTED_PROXIES` to the
proxy's source CIDR so audit-log IPs and the per-IP login throttle
key off the real client IP from `X-Forwarded-For` instead of the
proxy's own address:

```sh
# Single proxy on the same host:
KVM_CENTER_TRUSTED_PROXIES=127.0.0.1/32

# Reverse proxy on a private LAN:
KVM_CENTER_TRUSTED_PROXIES=10.0.0.0/8

# Multiple proxies (comma-separated):
KVM_CENTER_TRUSTED_PROXIES=10.0.0.5/32,10.0.0.6/32
```

When unset (default), kvm-center ignores `X-Forwarded-For` entirely
and trusts only the connection's `RemoteAddr`. **Set this on every
deployment that puts kvm-center behind a proxy** — without it,
audit-log entries record the proxy's IP for every request, which
makes per-tenant correlation impossible.

The reverse proxy must also forward the `Origin` and `Authorization`
headers, set `proxy_set_header Connection "upgrade"` (nginx) /
`Upgrade` keep-alive directives equivalent (Caddy/Traefik) for SSE
and the in-browser VNC console, and pass HTTPS upstream
(`KVM_CENTER_TLS_CERT`/`KVM_CENTER_TLS_KEY` set, terminated cert at
proxy is also OK).

If the SPA is hosted at a different origin from the API (rare —
typically when the proxy serves the SPA from a CDN and the API from
the kvm-center backend), declare the SPA origin via:

```sh
KVM_CENTER_CORS_ORIGINS=https://kvm.example.com
```

Default is empty (CORS closed entirely), which works for any
same-origin deployment.

## Verifying releases

Every release ships with a chain-of-custody artifact:

| Artefact | Signed by | Verified with |
|---|---|---|
| Docker image (`thedon21/kvm-center:vX.Y.Z`) | cosign keyless + key-pair | [`cosign verify`](#docker) |
| Bare-metal `kvm-center-server` binary | embedded build SHA | `kvm-center-server --version` matches the release tag |

The cosign public key lives in the repo so it's versioned alongside
the code that signed against it: [`docs/security/cosign.pub`](https://github.com/donovanm21/kvm-center/blob/main/docs/security/cosign.pub).

Quick reference:

```sh
cosign verify \
  --key https://raw.githubusercontent.com/donovanm21/kvm-center/main/docs/security/cosign.pub \
  thedon21/kvm-center:vX.Y.Z
```

If verification fails, treat the artefact as compromised — don't
install it, file an issue with the failing output, and pull again
from a different network path.

## Choosing a path

- **Just want to try kvm-center on your laptop?** Docker one-liner.
  Wipe later with `docker rm -f kvm-center && docker volume rm kvm-center-data`.
- **Single-host homelab, comfortable with Docker?** docker-compose.
  Mongo gives you the proper persistence layer; volume backups are
  trivial.
- **Custom Linux distro, ops shop with a config-management
  pipeline?** Bare-metal `install.sh`. The agent is a single 7 MB
  Go binary, the server is a single 30 MB binary; trivial to wrap
  in Ansible / Salt / whatever.

Either path gives you the same kvm-center; switching later is just
"point the new install at the same Mongo (or restore a backup) and
shut down the old one."

## Production trust model — agent TLS verification

The default agent build trusts any TLS certificate the server presents.
That's intentional for first-boot homelab pairing: the server's auto-
generated self-signed cert lands at `/var/lib/kvm-center/ca/server.crt`
on first run, and a fleet of agents reaching it via `https://kvm-center:8190`
can pair without needing the operator to push the CA out-of-band.

For any deployment where the server has a real CA-signed certificate
(public Let's Encrypt cert, internal corporate CA, ACME-issued cert
from a private root), you should flip agents to verifying mode:

```ini
# /etc/kvm-center/agent.env  (read by the systemd unit)
KVM_CENTER_SERVER=https://kvm-center.example.com:8190
KVM_CENTER_AGENT_VERIFY_TLS=1
# Optional — point at a CA bundle if the cert chain isn't in the
# system trust store (most internal CAs):
KVM_CENTER_SERVER_CA=/etc/kvm-center/server-ca.pem
```

Restart the agent (`systemctl restart kvm-center-agent`) and check the
journal — you should see:

```
kvm-center-agent: TLS verification ENABLED for https://kvm-center.example.com:8190 (system trust store)
```

instead of the default

```
⚠ kvm-center-agent: TLS verification DISABLED for ...
```

This applies uniformly to the agent's HTTP telemetry POSTs, gRPC
migration-progress stream, gRPC live-metrics stream, and known-hosts
sync — the trust decision is made once at startup and cached in
`agent/tls_trust.go`.

### What this protects against

A passive attacker on the network between agent and server cannot
read telemetry payloads — that's true even with the homelab default,
because the channel is TLS, just unverified. Verification specifically
defends against an **active** attacker who can intercept TCP and present
their own certificate (BGP hijack, ARP poison on a shared LAN segment,
malicious switch/router). With `InsecureSkipVerify: true`, the agent
would silently trust the impostor and stream telemetry, console-token
secrets, and (in future versions) command outcomes to it.

### Server→agent command channel

Commands flow the other direction over the agent's `:8081` listener.
Each request is **authenticated**: the server signs it with the shared
fleet secret (HMAC-SHA256, the same secret used for console tokens), and
the agent rejects unsigned or forged commands on its mutating routes.
Enforcement turns on automatically once an upgraded server advertises
that it signs, and the agent persists that state so it keeps enforcing
across restarts — a stale, non-signing server can never lock the fleet
out. In an emergency you can set `KVM_CENTER_DISABLE_AGENT_CMD_AUTH=1`
on the server to stop signing (agents then accept unsigned commands
again).

Agent **self-update** is signature-verified end to end: the server
publishes an Ed25519-signed manifest next to each agent binary, and the
agent verifies it against the grant key it already trusts before
swapping — and only ever downloads from its configured server, never a
caller-supplied URL.

The listener is still plain HTTP, so the signature gives integrity and
authenticity but not confidentiality. For deployments that need TLS in
both directions, front each agent with stunnel/nginx/Caddy on the same
hypervisor, terminate TLS, forward to `localhost:8081`, and bind the
agent's `:8081` to the loopback interface only.

The `kvmctl` CLI and in-guest kvm-tools default to skip-verify against
the self-signed control plane; set `KVMCTL_VERIFY_TLS=1` or
`KVM_TOOLS_VERIFY_TLS=1` to require certificate verification, mirroring
the agent's `KVM_CENTER_AGENT_VERIFY_TLS=1`.
