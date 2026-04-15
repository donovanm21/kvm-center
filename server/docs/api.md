---
title: API reference
section: Reference
order: 70
---

# API reference

Every UI action has a corresponding HTTP endpoint. This page is
hand-maintained — an auto-generated reference is on the roadmap.
All endpoints except the ones explicitly noted require
authentication (session cookie or `Authorization: Bearer kvmc_…`).

## Conventions

- Base URL: `https://<server>:8190`
- JSON in, JSON out. Content-Type: `application/json`.
- Errors: `{"error": "message"}` with an HTTP 4xx/5xx status.
- All state-changing endpoints write an audit event.

## Inventory

### `GET /api/hosts`
Returns the known hypervisors with telemetry and PKI state.

### `GET /api/hosts/<id>`
Single host detail.

### `GET /api/vms?host=<id>&search=<q>`
All VMs, optionally filtered by host or a free-text substring.

### `GET /api/storage?host=<id>`
Storage pools — capacity, allocation, available, state, path, type.
Telemetry-backed; refreshes per-tick.

## Commands

### `POST /api/command`
The unified command path. Body:

```json
{
  "host_id": "hvnode01.example.com",
  "vm_uuid": "…",
  "vm_name": "web01",
  "action": "START",
  "params": { "destination": "10.0.0.12" }
}
```

Actions: `START`, `STOP`, `RESTART`, `FORCESTOP`, `SNAPSHOT`,
`REVERT_SNAPSHOT`, `DELETE_SNAPSHOT`, `MIGRATE`, `OFFLINE_MIGRATE`,
`DELETE_VM`.

`MIGRATE` / `OFFLINE_MIGRATE` honour optional params:

- `migration_transport`: `"tls"` / `"ssh"` / empty (auto)
- `target_pool`: destination storage pool for offline (default: "default")
- `new_vm_name`: rename on destination (offline only)
- `regenerate_mac`: `"true"` to emit fresh 52:54:00 MACs
- `keep_raw`: `"true"` to round-trip raw → qcow2 → raw
- `copy_storage`: `"true"` to include disks in live migrations

## Migrations

### `GET /api/migrations`
Newest-first list of running, completed, failed migrations.

### `POST /api/migrations/cancel?id=<id>`
Graceful abort. Requires `vm.migrate`.

### `GET /api/migrate-status`
Live progress from the current source agent.

## Snapshots

### `GET /api/vms/snapshots?host=<id>&vm=<name>`
Proxies the agent's `/snapshots/<vm>` endpoint.

## Hypervisor PKI (v5.10+)

### `POST /api/ca/enroll`
Admin-only. Body: `{"host_id": "hvnode01.example.com"}`. Returns a
one-time enrollment token (10-min TTL).

### `POST /api/ca/sign`
Public — gated by a valid enrollment token. Body:
`{"enroll_token": "enroll_…", "csr": "-----BEGIN CERTIFICATE REQUEST-----\n…"}`.
Returns the signed leaf + CA bundle.

### `GET /api/ca/bundle`
CA public certificate (PEM).

### `GET /api/ca/hypervisors`
List issued certs with revocation state.

### `POST /api/ca/hypervisors/<host>/revoke`
Mark a cert revoked. Body: `{"reason": "…"}`.

## Grants (v5.11+)

### `GET /api/migrations/grant-pubkey`
Public. Returns the Ed25519 signer's public key as hex.

## Tokens

### `GET /api/tokens` / `POST /api/tokens`
Admin only — system tokens.

### `GET /api/tokens/me` / `POST /api/tokens/me`
Any signed-in user — their own tokens.

### `DELETE /api/tokens/<id>` (admin) / `DELETE /api/tokens/me/<id>` (self)
Revoke.

## Audit

### `GET /api/audit?action=<a>&actor=<a>&resource=<r>&since=<unix>&until=<unix>&success=true|false&limit=100&offset=0`
Filter + paginate. Newest first.

## Settings

- `GET/PUT /api/settings/notifications` + `POST /api/settings/notifications/test`
- `GET/PUT /api/settings/metrics` + `POST /api/settings/metrics/test`
- `GET/PUT /api/settings/audit-shipping` + `POST /api/settings/audit-shipping/test`
- `GET/POST /api/settings/database/{status,test,migrate}`

## TLS management

- `GET /api/tls/status`
- `POST /api/tls/upload`
- `POST /api/tls/regenerate`
- `GET /api/tls/cert.pem`

## Docs

- `GET /api/docs/tree` — list all pages (slug, title, section, order)
- `GET /api/docs/page/<slug>` — rendered HTML + TOC

## Versioning + health

- `GET /api/version` — `{"version": "v5.14.0"}`. Public.
- `GET /health` — `{"status": "ok"}`. Public.
- `GET /api/portal/health` — server-side probe of the build portal.
