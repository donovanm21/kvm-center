<div align="center">

<img src="docs/assets/logo.svg" alt="kvm-center" width="112" height="112">

<img src="docs/assets/wordmark.svg" alt="kvm center" height="44">

**A single pane of glass for your KVM hypervisor fleet.**

Inventory, live + offline migrations, snapshots, VNC consoles,
mTLS between hosts, signed per-migration grants, in-app docs,
Influx-backed metrics — all in one Go binary and a tiny agent.

<img src="docs/assets/kvm-center-tour.webp" alt="kvm-center full UI tour — dashboard, hosts, VMs, snapshots, storage, migrations, System Health, docs" width="720">

[Quick start](#quick-start) · [Documentation](server/docs/overview.md) · [Architecture](server/docs/architecture.md) · [Changelog](server/docs/changelog.md)

[![CI](https://github.com/donovanm21/kvmcenter/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/donovanm21/kvmcenter/actions/workflows/test.yml)
![Go](https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square&logo=go&logoColor=white)
![React](https://img.shields.io/badge/React-18-61DAFB?style=flat-square&logo=react&logoColor=white)
![libvirt](https://img.shields.io/badge/libvirt-qemu%2Btls-E3571E?style=flat-square)
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

---

## Quick start

Interactive installer (recommended for fresh hosts — walks you
through Mongo creds and the cluster DNS domain, writes `.env`
with mode `0600`, then deploys):

```sh
git clone <repo> /opt/kvm-center
cd /opt/kvm-center
scripts/bootstrap.sh --interactive
```

Re-running `--interactive` on an already-installed host opens a
state-aware menu — **keep** (safe redeploy), **reconfig**,
**fresh** (destructive nuke, double-confirmed), or **cancel** —
so you can't silently clobber a working stack.

For automated redeploys (CI, SSH loops) skip the TUI:

```sh
scripts/bootstrap.sh --deploy
```

Either path brings the stack up on port **8190**. Open it in a
browser, create the admin account, and the built-in **setup
wizard** steps you through enrolling your first hypervisor plus
optional notifications + metrics — every step is skippable.

See [**Getting started**](server/docs/getting-started.md) for the
full walkthrough, TLS options, and `kvmctl` setup.

---

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

---

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

Full diagram and trust boundaries:
[**Architecture**](server/docs/architecture.md).

---

## Documentation

The canonical docs ship with the server binary and render under
**`/docs`** in the running UI (Help icon, top-right). Source
markdown lives next to the code so a release binary can never
drift out of sync with its own help site.

| Page | Source |
|------|--------|
| Overview | [`server/docs/overview.md`](server/docs/overview.md) |
| Getting started | [`server/docs/getting-started.md`](server/docs/getting-started.md) |
| Architecture | [`server/docs/architecture.md`](server/docs/architecture.md) |
| Authentication (sessions, API tokens, IdPs, 2FA) | [`server/docs/authentication.md`](server/docs/authentication.md) |
| Migrations (mTLS channel + signed grants) | [`server/docs/migrations.md`](server/docs/migrations.md) |
| Networking & firewall | [`server/docs/networking.md`](server/docs/networking.md) |
| Remote access | [`server/docs/remote-access.md`](server/docs/remote-access.md) |
| `kvmctl` CLI | [`server/docs/kvmctl.md`](server/docs/kvmctl.md) |
| API reference | [`server/docs/api.md`](server/docs/api.md) |
| Troubleshooting | [`server/docs/troubleshooting.md`](server/docs/troubleshooting.md) |
| Changelog (no dates, newest first) | [`server/docs/changelog.md`](server/docs/changelog.md) |

### Contributors

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for how to propose changes,
[`SECURITY.md`](SECURITY.md) for private vulnerability reporting, and
[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) for the ground rules.
Development and style guides are shared directly with contributors
once they're onboarded.

---

## Support

kvm-center is maintained as a side project. Bug reports and pull
requests are welcome via GitHub issues; response times vary and
there's no commercial SLA. For security issues, please use the
private reporting channel described in
[`SECURITY.md`](SECURITY.md) rather than a public issue.

---

## Editorial policy

A feature commit that changes operator-visible behaviour updates
[`server/docs/`](server/docs/) **in the same commit**, not a
follow-up. Every tagged release adds an entry to
[`server/docs/changelog.md`](server/docs/changelog.md) — no dates,
version heading only.

---

## License

kvm-center is released under the [Apache License 2.0](LICENSE). You
are free to use, modify, and redistribute it, including commercially,
subject to the terms of that license — which includes an explicit
patent grant from contributors.

<div align="center">

Built with Go, React, and a deep suspicion of appliances.

</div>
