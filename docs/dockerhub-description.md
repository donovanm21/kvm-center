<div align="center">

<img src="https://kvm-center.com/assets/logo.svg" alt="kvm-center" width="112" height="112">

<img src="https://kvm-center.com/assets/wordmark.svg" alt="kvm center" height="44">

**A single pane of glass for your KVM hypervisor fleet.**

Inventory, live + offline migrations, snapshots, VNC consoles,
mTLS between hosts, signed per-migration grants, in-app docs,
Influx-backed metrics — all in one Go binary and a tiny agent.

<img src="https://kvm-center.com/assets/dashboard-hero.webp" alt="kvm-center dashboard — 30 hypervisors, 147 VMs, fleet activity, telemetry" width="720">

[Source](https://github.com/donovanm21/kvm-center) ·
[Documentation](https://github.com/donovanm21/kvm-center/blob/main/server/docs/overview.md) ·
[Architecture](https://github.com/donovanm21/kvm-center/blob/main/server/docs/architecture.md) ·
[Changelog](https://github.com/donovanm21/kvm-center/blob/main/server/docs/changelog.md)

![Multi-arch](https://img.shields.io/badge/arch-amd64%20%7C%20arm64-3FB950?style=flat-square)
![Cosign signed](https://img.shields.io/badge/cosign-signed-3FB950?style=flat-square)
![Go](https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square&logo=go&logoColor=white)
![License](https://img.shields.io/badge/license-Apache--2.0-blue?style=flat-square)
![Status](https://img.shields.io/badge/status-active-3FB950?style=flat-square)

</div>

---

## Why kvm-center

Running KVM directly is great. Running ten of them is a chore —
SSHing between hosts, juggling `virsh`, hand-copying disks,
remembering which pool lives where. kvm-center gives you the
central control surface KVM deserves, without the appliance,
without the licensing, and without the footprint.

- **One server, many hypervisors.** A stateless Go control plane
  and a ~7 MB agent per host. MongoDB for persistence, nothing else.
- **Migrations that just work.** Live or offline, over native
  `qemu+tls://` with Ed25519-signed per-migration grants. No SSH
  key sprawl, no shared NFS assumed.
- **Snapshots, consoles, metrics.** Per-VM snapshot timelines,
  in-browser VNC, InfluxDB line-protocol metrics with a downloadable
  Grafana dashboard.
- **Auth that fits your org.** Local users, API tokens, LDAP, SAML,
  OIDC, and 2FA — all in the same binary.
- **Docs that ship with the code.** `server/docs/*.md` is embedded
  and rendered in-app. The Help icon in the top bar is how your
  operators learn the product.

## Quick start

### One-liner (no Mongo — JSON file persistence)

```sh
docker run -d --name kvm-center -p 8190:8443 \
  -v kvm-center-data:/var/lib/kvm-center \
  -e KVM_CENTER_PUBLIC_URL=https://localhost:8190 \
  thedon21/kvm-center:latest
```

Open `https://localhost:8190`, accept the self-signed cert, walk the
captive setup wizard. Backed by a named Docker volume — back it up
like any other volume.

### docker-compose (with MongoDB — recommended)

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

```sh
MONGO_PASSWORD=$(openssl rand -base64 24) docker compose up -d
docker compose logs -f server
```

### Auto-update with Watchtower (optional)

Tracking a rolling tag (`:latest`, `:5`, `:5.49`)? Add a Watchtower
service and label your kvm-center container `com.centurylinklabs.watchtower.enable=true`.
It polls Docker Hub once an hour and restarts on a fresh push. Pin
`:vX.Y.Z` if you don't want automatic upgrades.

Full recipe in [Deployment → Auto-update with Watchtower](https://github.com/donovanm21/kvm-center/blob/main/server/docs/deployment.md#auto-update-with-watchtower).

## What's in the box

| Surface | What it does |
|---|---|
| **Dashboard** | Fleet-wide CPU / memory / storage rollup, health pills per host, recent migrations. |
| **Hosts** | Per-hypervisor telemetry, mTLS enrollment state, storage pools, network bridges. |
| **VMs** | Start / stop / restart / migrate / snapshot / console — sortable, searchable, bulk-selectable. |
| **Migrations** | Live + offline, transport toggle (mTLS / SSH), advanced modal for pool picker, VM rename, MAC regen. |
| **Snapshots** | Per-VM timelines, fleet-wide snapshot overview, create / revert / delete. |
| **Metrics** | Influx v2 sink, Grafana dashboard JSON served from `/api/grafana/dashboard.json`. |
| **Audit** | Every state-changing action, filterable, shippable to syslog / HTTP. |
| **Settings** | Users, IdPs, API tokens, TLS management, notification channels, DB status. |
| **Docs** | Embedded help site under `/docs`, rendered from `server/docs/*.md`. |
| **kvmctl** | Cross-platform CLI for scripting against the same API. |

## Tag channels

| Tag | Tracks |
|---|---|
| `:latest` | last published minor / major release (rolling) |
| `:5.49` | last 5.49.x patch (stable channel) |
| `:5` | last 5.x release |
| `:vX.Y.Z` | exact release, immutable |

Pin `:vX.Y.Z` for production; pin `:5.49` if you want patch updates
without minor-version surprises.

## Architecture

```
Browser ─── :8190 ──▶  kvm-center-server (Go)
                       │   HTTPS · session / bearer
                       ▼
                  MongoDB persistence
                       ▲
                       │   HTTPS
                       ▼
           ┌────────────┬────────────┐
           │            │            │
        hv01:8081    hv02:8081    hv03:8081
        (kvm-center-agent systemd)
           ↕            ↕            ↕
           └── qemu+tls :16514 (east-west) ──┘
                signed grants · libvirt hook enforced
```

Full diagram and trust boundaries in
[Architecture](https://github.com/donovanm21/kvm-center/blob/main/server/docs/architecture.md).

## Verifying the image (cosign)

Every published image is cosign-signed against the public key
versioned in the source repo at
[`docs/security/cosign.pub`](https://github.com/donovanm21/kvm-center/blob/main/docs/security/cosign.pub):

```sh
cosign verify \
  --key https://raw.githubusercontent.com/donovanm21/kvm-center/main/docs/security/cosign.pub \
  thedon21/kvm-center:vX.Y.Z
```

Refuse to run any image whose signature doesn't verify.

## Architectures

`linux/amd64`, `linux/arm64`. Apple silicon Macs (M1/M2/M3/M4) pull
the `arm64` manifest natively. Add `--platform linux/amd64` if you
hit anything that genuinely needs x86 (rare).

## Other ways to deploy

Docker is the fastest path; for hosts where you want to skip the
container layer the **bare-metal `bootstrap.sh --interactive`** flow
gets you the same kvm-center on the host filesystem. Switching later
is "point the new install at the same Mongo and shut the old one
down." See
[Deployment](https://github.com/donovanm21/kvm-center/blob/main/server/docs/deployment.md).

## Community & support

kvm-center is maintained as a side project — async, operator-run.

- **Discord:** [discord.gg/c6yWauUsQh](https://discord.gg/c6yWauUsQh) — install help, mTLS pairing, release announcements.
- **Issues / PRs:** [GitHub issues](https://github.com/donovanm21/kvm-center/issues) (no commercial SLA).
- **Security:** see [`SECURITY.md`](https://github.com/donovanm21/kvm-center/blob/main/SECURITY.md) for the private channel — please don't file vulnerabilities in public issues.

## License

[Apache License 2.0](https://github.com/donovanm21/kvm-center/blob/main/LICENSE).
Free for use, modification, and redistribution — including
commercially — with the standard Apache 2 patent grant from
contributors.

<div align="center">

Built with Go, React, and a deep suspicion of appliances.

</div>
