# KVM Center

A centralised management platform for KVM hypervisor fleets —
single pane of glass for VMs, hosts, snapshots, migrations,
notifications, metrics, and audit. Built in Go + React. Stateless
server, tiny Go agents, MongoDB persistence, mutual-TLS-authenticated
migrations with signed per-migration grants.

## Documentation

The canonical documentation ships with the server binary and is
served under **`/docs`** in the running UI (Help icon top-right).
Source markdown lives at [`server/docs/`](server/docs/) — edits
travel with the release binary.

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

### Contributor docs

These stay at the repo root — different audience, rarely read by
operators:

- [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) — working on
  kvm-center itself (local build, test harness, release process).
- [`docs/STYLE_GUIDE.md`](docs/STYLE_GUIDE.md) — frontend design
  tokens and component conventions.

## Editorial policy

A feature commit that changes operator-visible behaviour updates
[`server/docs/`](server/docs/) in the same commit, not a follow-up.
Every tagged release adds an entry to
[`server/docs/changelog.md`](server/docs/changelog.md) — no dates,
version heading only.

## Quick start

```sh
git clone <repo> /opt/kvm-center
cd /opt/kvm-center
scripts/bootstrap.sh --deploy
```

See [Getting started](server/docs/getting-started.md) for the rest.

## Architecture at a glance

```
Browser → :8190 → kvm-center-server (Go)
                     ↑ HTTPS + session / bearer token
                     ↓ HTTPS/HTTP
              hvnode01:8081  hvnode02:8081  …
              (kvm-center-agent systemd service)
                    ↕ qemu+tls :16514 between agents
                      (signed grants, libvirt hook enforced)
```

Full diagram + trust boundaries in
[architecture](server/docs/architecture.md).
