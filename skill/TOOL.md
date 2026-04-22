# kvm-center — tool schemas

Every tool authenticates with `Authorization: Bearer $KVMCTL_TOKEN`
against `$KVMCTL_SERVER` (default `https://kvm-center:8190`). TLS
verification is skipped by default (self-signed cert); swap to a
verifying client if the operator has installed a trusted cert.

Tools use the server's REST API. `kvmctl` and the SPA drive the same
endpoints, so anything the SPA can show, a tool can read.

---

## `list_hosts`

**HTTP:** `GET /api/hosts`

**Returns:** `[Host]` — each with `id`, `ip`, `status`, `last_seen`,
`agent_version`, `cpu_count`, `cpu_usage`, `mem_usage`, `vm_count`,
`maintenance_mode`, `mtls_ready`.

**Use when:** enumerating hypervisors, checking fleet status, picking
a migration target.

---

## `list_vms`

**HTTP:** `GET /api/vms[?host=<id>&search=<q>]`

**Params:**
- `host` — optional; filter to one hypervisor's VMs.
- `search` — optional; case-insensitive substring match on name/UUID.

**Returns:** `[VM]` with `uuid`, `name`, `host_id`, `state`,
`cpu_count`, `mem_total_mb`, `disk_total_gb`, `disk_usage_gb`,
`cpu_usage`, `mem_usage_mb`, `guest_os`, `kvm_tools_version`,
`snapshot_count`, `snapshots_blocked_reason`.

---

## `vm_detail`

**HTTP:** `GET /api/vms/{uuid}`

**Returns:** full `VM` object including `all_disks`, `networks`,
`pci_devices`, `firmware`, `machine_type`, `boot_dev`, `tpm_version`,
`video_model`.

---

## `vm_action`

**HTTP:** `POST /api/command`

**Body:**
```json
{
  "host_id":   "hvnode02",
  "vm_uuid":   "uuid-...",
  "vm_name":   "VM1",
  "action":    "START|STOP|FORCESTOP|RESTART|POWER_CYCLE|
                SNAPSHOT|REVERT_SNAPSHOT|DELETE_SNAPSHOT|
                MIGRATE|OFFLINE_MIGRATE|DELETE_VM|EDIT_VM|CHANGE_MEDIA",
  "params":    { "...action-specific..." }
}
```

**Destructive actions require explicit user confirmation in chat.**

Key `params`:
- `SNAPSHOT`: `name` (auto-generated if omitted — `snap-<unix>`).
- `MIGRATE` / `OFFLINE_MIGRATE`: `destination` (IP or host ID).
- `EDIT_VM`: `vcpus`, `mem_mb`, etc.
- `CHANGE_MEDIA`: `media_action` (`eject`|`insert`), `target`, `path`.

**Idempotency:** server auto-injects a `request_id`. Retries will
short-circuit to the cached outcome; no double-execute.

**Returns:** `{success, message, requires_reboot?, updated_fields?,
request_id, replayed?}`.

---

## `list_migrations`

**HTTP:** `GET /api/migrations[?status=running|complete|failed]`

**Returns:** `[MigrationRecord]` with `id`, `vm_name`, `source_host`,
`dest_host`, `mode` (live/offline), `transport` (ssh/tls), `status`,
`started_at`, `completed_at`, `elapsed_sec`, `progress`, `phase`,
`error`.

Live migrations also carry `progress_pct` updated in real time.

---

## `list_snapshots`

**HTTP:** `GET /api/vms/snapshots?host=<host>&vm=<name>`

**Returns:** `[Snapshot]` — `{name, description, parent, state,
creation_time}`.

---

## `create_snapshot`

Thin wrapper over `vm_action` with `action=SNAPSHOT`. Keep it
distinct in the SKILL.md tool list because the safe-ish nature
(storage burn but no data loss) lets read-mostly chats call it
with one confirmation.

---

## `system_health`

**HTTP:** `GET /api/admin/system-health`

**Returns:** `SystemHealth` — rollup `overall` + per-subsystem cards
(control_plane, database, tls, hypervisors, guest_tools, migrations,
snapshots, command_log). Each card has `status` (green/amber/red),
`headline`, `detail` map.

**Admin-only.** 403 if the token lacks `user.manage`.

Cache: server caches 15 s internally; polling is cheap.

---

## `audit_search`

**HTTP:** `GET /api/audit/events?actor=<id>&action=<verb>&from=<ts>&limit=<n>`

**Params:** All optional. `limit` caps at 500 by default.

**Returns:** `[AuditEvent]` — `{id, at, actor_id, actor_name,
action, resource, detail, success, error}`.

**Admin-only.**

---

## `reconcile_host`

**HTTP:** `POST /api/admin/reconcile/{host_id}`

Forces the target agent to POST telemetry immediately and returns a
diff against the prior snapshot: `added_vms`, `removed_vms`,
`state_changed`, before/after `last_seen`, `agent_status` (refreshed
/ unreachable / skipped).

**Admin-only.** Use during post-incident triage or when the user
asks "is what I'm seeing fresh?"

---

## `force_fail_migration`

**HTTP:** `POST /api/migrations/force-fail?id=<migration_id>&reason=<text>`

Marks a stuck migration record as `failed` with the supplied reason.
Does NOT contact the agent — the operator has confirmed the VM state
out-of-band. Use as last resort.

**Admin-only.** Confirm in chat before calling.

---

## Error model

All endpoints return `{"error": "..."}` on 4xx/5xx. Inspect
`resp.status` + that field and surface a one-line summary. Don't
retry on 4xx — the payload is wrong or auth is missing.

Common codes:
- 401 "not authenticated" — token missing/invalid
- 403 "missing permission: ..." — operator's role lacks the bit
- 404 — target VM/host not found
- 409 — invariant violation (maintenance mode blocks migration,
  etc.). Relay the message verbatim; it's always operator-actionable.
- 502 — agent unreachable
