---
title: Architecture
section: Getting started
order: 30
---

# Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                 kvm-center server (Go)                          │
│                                                                 │
│  • Cluster CA (ECDSA P-256, 10-year root)                       │
│  • Ed25519 grant signer                                         │
│  • MongoDB backend (JSON fallback)                              │
│  • HTTPS SPA + API on :8190, plain HTTP for agents on :8090     │
│  • Notifications, metrics sink, audit shipping                  │
└─────────────────────┬───────────────────────────────────────────┘
                      │ HTTPS + bearer tokens / session cookies
      ┌───────────────┼───────────────┐
      │               │               │
 ┌────▼────┐     ┌────▼────┐     ┌────▼────┐
 │ kvmctl  │     │ SPA     │     │ Agents  │ :8081
 │ CLI     │     │ browser │     │ per hv  │
 └─────────┘     └─────────┘     └────┬────┘
                                       │ forks virsh, serves /grant-active,
                                       │ proxies VNC console, ships telemetry
                                       │
                                  ┌────▼─────────┐
                                  │ libvirtd     │ :16514 (qemu+tls)
                                  │ /hooks/qemu  │
                                  └──────────────┘
```

## Roles

**Server** — authoritative source of truth. Holds the cluster CA,
Ed25519 grant signing key, user store, roles, tokens, audit log,
migration records. Runs in a Docker container on one host. Scales
by moving MongoDB behind a replica set; the server itself is
stateless (everything in Mongo).

**Agent** — one per hypervisor. Tiny binary (~7 MB) written in Go,
no runtime deps beyond `virsh` and standard tools. Tasks:

- Ship host + VM telemetry every 30 s.
- Execute commands: start / stop / restart / migrate / snapshot /
  delete, storage browse / upload / download.
- Proxy the VNC console through its own HTTP listener so the server
  never opens a direct connection to a QEMU VNC socket.
- Drive offline migrations (scp or `virsh vol-upload` over qemu+tls).
- Validate signed migration grants, cache them for the libvirt hook.

**Hypervisor** — runs libvirtd + QEMU + the agent. Enrolled via a
one-time token; receives a leaf cert signed by the cluster CA.

## Trust boundaries

| Boundary | Authentication | Transport |
|----------|----------------|-----------|
| Browser / CLI → Server | Session cookie or API token | HTTPS (self-signed or operator-provided cert) |
| Agent → Server (telemetry) | Host identity via TLS posture + telemetry content | HTTP:8090 plaintext (ok — telemetry is pulled/pushed; no secrets exchanged) |
| Server → Agent (commands) | None today — agents trust the server because they're on the management network; tightening in a future release | HTTP:8081 |
| Agent ↔ Agent (migration data) | mTLS with cluster-CA-signed leaf certs + Ed25519-signed per-migration grant | qemu+tls on :16514 + data on 49152-49215 |

## Data flow: a live migration

1. Operator clicks migrate in the SPA or runs `kvmctl vm migrate`.
2. Server's `/api/command` handler selects the transport (mTLS when
   both hosts are enrolled; SSH fallback otherwise) and mints an
   Ed25519-signed grant bound to `{src, dst, vm, 5-min TTL}`.
3. Grant rides as a command param to the source agent.
4. Source agent POSTs the envelope to the destination agent's
   `/prepare-grant`. Destination validates signature, src/dst
   binding, and TTL against the cached server public key; records
   single-use in `/var/lib/kvm-center/consumed-grants.log`.
5. Source agent runs `virsh migrate --live --undefinesource <vm>
   qemu+tls://<dest>/system`.
6. Destination libvirtd accepts the migration; `/etc/libvirt/hooks/qemu`
   fires on "prepare begin" and dials the local agent's
   `/grant-active?vm=<name>` to confirm the migration was authorised.
   Non-zero exit aborts libvirtd's acceptance.
7. QEMU streams the VM state over the mTLS-authenticated channel.
8. Source definition is removed via `--undefinesource`.

Audit trail: `migration.grant.issue` (server) → `migration.start`
(server) → `migration.grant.accept` (implicit via agent log).

## Data flow: an offline migration

v5.11.6+ uses the same qemu+tls channel via `virsh vol-upload` when
both hosts are enrolled (no scp on the hot path). Legacy `scp` path
stays available for mixed fleets. See
[Migrations](/docs/migrations) for the full state machine.
