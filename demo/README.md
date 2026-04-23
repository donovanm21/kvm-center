# kvm-center demo deployment

> ⚠ **Dev-only.** This entire `demo/` tree is stripped from the public
> GitHub mirror during the scrub publish step (see
> `memory/feedback_github_mirror.md` in the session memory for the
> updated `git-filter-repo` invocation). It lives on the internal
> Gitea repo only.

A one-command local stack that brings up a fully populated
kvm-center instance with **synthetic fixture data** — no real
hypervisors required. Used for:

- Drafting documentation against a non-empty UI
- Recording walkthrough clips for in-app / online docs
- Demoing features end-to-end without touching prod
- Later: harness for end-to-end Playwright tests

## What's inside

| Collection        | Count | Notes                                                                                          |
|-------------------|-------|------------------------------------------------------------------------------------------------|
| Hypervisors       | 30    | `hv-demo01..30.example.lan` on `192.0.2.11-40` (RFC5737 docs-only range). Tiered: 5 small edge nodes, ~19 medium workhorses, 3 large compute boxes, 3 GPU hosts |
| VMs               | ~150  | 4-6 per host, mix of web/api/db/dev/CI/staging/Windows/ML archetypes                           |
| Storage pools     | 30-33 | One default per host + nvme-fast on GPU/large hosts                                            |
| Migrations        | 200   | Across the last 30 days, mostly complete, some failed/cancelled                                |
| Audit events      | 800   | Spread across admin/operator/viewer actors                                                     |
| Snapshot policies | 4     | 3 active (web hourly + db daily + api weekly), 1 paused                                        |
| Command log       | 100   | Mostly success; 3 "unknown" stranded entries for the health-problems card                      |
| API tokens        | 1     | Fixed `kvmc_demo0001...` so predictable across resets                                          |
| Users / Groups    | 3 / 2 | `admin`, `operator`, `viewer` with matching role bundles                                       |
| Notifications     | 3 ch. | Slack webhook + PagerDuty webhook + disabled email — exercise the full config UI               |
| Hypervisor certs  | 30    | One per host, 10-year validity                                                                 |

Every hostname is `*.example.lan`; every IP is in the RFC5737
documentation ranges (`192.0.2.0/24`, `198.51.100.0/24`,
`203.0.113.0/24`). Safe to screenshot.

## Quick start

```sh
./scripts/demo-up.sh
```

First run builds the production server image (one-time, ~2 min)
plus the demo image, then boots the stack. Subsequent runs reuse
the cached images.

Open **https://localhost:8290** (accept the self-signed TLS warning).

Credentials:

| Role     | Username   | Password         |
|----------|------------|------------------|
| Admin    | `admin`    | `demo-password`  |
| Operator | `operator` | `demo-operator`  |
| Viewer   | `viewer`   | `demo-viewer`    |

API token for `kvmctl` / Claude skill testing:

```
kvmc_demo0001.00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff
```

## Reset

One-shot re-seed (drops every collection, re-inserts fixtures):

```sh
./scripts/demo-reset.sh
```

Auto-reset daily at 03:00 via cron — add to your dev host's crontab
once:

```cron
0 3 * * * /path/to/kvm-center/scripts/demo-reset.sh >/tmp/kvmc-demo-reset.log 2>&1
```

## Teardown

```sh
docker compose -f demo/docker-compose.demo.yml down -v
```

The `-v` flag removes the named volumes (`kvm-center-demo-mongo`,
`kvm-center-demo-tls`, `kvm-center-demo-ca`, `kvm-center-demo-data`)
so nothing lingers.

## Stage roadmap

- **v5.33.7 (this stage)** — static fixtures + no-op commands.
  Metrics are the numbers that came out of the seed and don't
  move; clicking Start/Stop returns success but doesn't animate
  anything.
- **Stage 2** — per-metric drift so sparklines move; `/command`
  flips in-memory VM state in the demo-agent so the SPA reflects
  the action.
- **Stage 3** — scripted scenarios on a 30-minute loop: load
  spike, live migration with gRPC progress, host offline blip,
  notifications firing, snapshot policies firing.

## Ports

| Port  | Use                              |
|-------|----------------------------------|
| 8190  | prod server (don't conflict)     |
| 8290  | demo server (this stack)         |
| 9190  | test server (kvmc-test)          |
