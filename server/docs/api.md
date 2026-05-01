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
  "host_id": "hv01.example.com",
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

### Agent endpoints (internal)

These endpoints live on the agent's command port (`:8081`). Operators
do not normally call them directly — they exist for the server's
control loop. They are documented here because operators sometimes
script against them when debugging or building tooling.

#### `GET /command-outcome?request_id=<id>` (v5.47.2+)

Read-only lookup against the agent's outcome cache. Used by the
server's reconciler when an initial `POST /command` response was
lost (transport error, timeout, or 5xx). The cache holds the agent's
reply for 35 minutes; pre-5.47.2 agents do not implement this path
and return 404.

```json
GET /command-outcome?request_id=req_abc123
200 OK
{
  "request_id": "req_abc123",
  "outcome": "success",
  "status_code": 200,
  "message": "VM started",
  "cached_at": "2026-04-30T22:45:01Z"
}
```

`outcome` is `"success"` for 2xx, `"failed"` for 4xx, `"unknown"`
for unexpected codes. On `failed`, `error` carries the agent's
error string (extracted from the cached body's `error` or `message`
field). `cached_at` is when the agent finished the action and stored
its reply.

`404` is returned when the `request_id` is not in cache (never
reached the agent, or the agent restarted, or the 35-minute window
elapsed). The reconciler treats 404 as "no opinion, keep waiting"
until its own deadline expires.

### SSE event `command` (v5.47.2+)

The server's live command reconciler emits this event whenever it
resolves a pending command-log entry to a terminal state. Clients
subscribed to `/api/events` (the existing SSE stream) receive:

```json
{
  "type": "command",
  "payload": {
    "request_id": "req_abc123",
    "outcome": "success",
    "host_id": "hv01",
    "action": "DELETE_SNAPSHOT"
  }
}
```

Use it to refresh the command-log page or per-VM badges without
re-polling. Per-poll attempts (404, network errors) are *not*
emitted — the event fires only on terminal transitions.

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
Admin-only. Body: `{"host_id": "hv01.example.com"}`. Returns a
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

## User management

All require `user.manage` (admin role by default).

### `GET /api/users`
List every account. Returns sanitised user records (no password
hashes, no TOTP secrets).

### `POST /api/users`
Create a new local user. Body: `{"username", "email", "password",
"role_ids": ["role_admin"], "enabled": true}`.

### `PUT /api/users/<id>`
Update name, email, enabled flag, or role list. Cannot disable
the last enabled admin — `409` is returned in that case.

### `DELETE /api/users/<id>`
Delete a user. Same last-admin guard as `PUT`. Audit row written.

### `POST /api/users/<id>/reset-password`
Set a new password without knowing the old one. Body:
`{"password": "..."}`. The acting user (token or session) needs
`user.manage`; the change is audited as the acting user, not the
target.

## Group + role management

### `GET/POST /api/groups`, `PUT/DELETE /api/groups/<id>`
Groups bundle role IDs and a member list. Used by LDAP auth to
map directory groups onto kvm-center roles.

### `GET/POST /api/roles`, `PUT/DELETE /api/roles/<id>`
A role is a name + a list of permission strings (`vm.console`,
`host.manage`, etc.). The built-in roles (`role_admin`,
`role_operator`, `role_viewer`) are read-only.

### `GET /api/permissions`
Static list of every permission string the server knows about,
keyed by category. Used by the UI to populate the role editor.

## Settings

- `GET/PUT /api/settings/notifications` + `POST /api/settings/notifications/test`
- `GET/PUT /api/settings/metrics` + `POST /api/settings/metrics/test`
- `GET/PUT /api/settings/audit-shipping` + `POST /api/settings/audit-shipping/test`
- `GET /api/settings/database/status` — backend kind (mongo / json),
  connection state, last-write timestamp, store size in bytes
- `POST /api/settings/database/test` — pings the configured backend
- `POST /api/settings/database/migrate` — admin-only; trigger a
  one-shot JSON → Mongo migration (no-op if already on Mongo)

## TLS management

- `GET /api/tls/status`
- `POST /api/tls/upload`
- `POST /api/tls/regenerate`
- `GET /api/tls/cert.pem`

## Docs

- `GET /api/docs/tree` — list all pages (slug, title, section, order)
- `GET /api/docs/page/<slug>` — rendered HTML + TOC

## First-boot setup wizard (v5.20+)

- `GET /api/setup/state` — **public**. Returns `{complete, completed_at,
  has_any_host}`. The SPA reads this before login so it can redirect
  an admin into the wizard on a fresh install. No secrets in the
  response.
- `POST /api/setup/complete` — admin (`user.manage`). Marks the
  wizard finished so the SPA stops redirecting. Backfilled to
  `complete=true` at boot on any upgrade path that already has an
  admin user, so existing installs never see the wizard.

## Versioning + health

- `GET /api/version` — `{"version": "vX.Y.Z"}`. Public.
- `GET /health` — `{"status": "ok"}`. Public.
- `GET /downloads/*` — agent / kvm-tools / kvmctl binaries + install scripts (unauthenticated; enrollment token is the real gate on cluster access).
