---
name: kvm-center
description: Manage a KVM hypervisor fleet through the kvm-center control plane — list/inspect hosts and VMs, drive lifecycle actions, create snapshots, start migrations, check system health. Use when the user asks about VM state, hypervisor status, migrations, snapshots, or any homelab virtualisation concept that kvm-center manages.
version: v5.33.0
---

# kvm-center

You have access to a kvm-center control plane that manages a fleet of
KVM/libvirt hypervisors. It is the operator's vCenter-lite.

## When to invoke

Invoke when the user asks about any of the following:
- VM inventory, power state, hardware config (vCPU / memory / disk)
- Hypervisor (host) health, agent version, maintenance mode
- Snapshots, snapshot policies, snapshot revert
- Migrations (live or offline) between hypervisors
- System health (is the control plane OK? are any certs expiring?)
- Command-log replay / stranded commands

Skip this skill for non-kvm-center topics. If the user is asking a
generic Linux/libvirt/virsh question unrelated to their deployment,
answer directly without calling any tool.

## Setup

Tools here use a kvm-center API token. First call:
1. Read `~/.config/kvm-center/config.json` (created by `kvmctl login`).
   Expected shape: `{"server": "https://...", "token": "kvmc_..."}`.
2. If the file is absent, ask the user to run `kvmctl login` or to
   paste `KVMCTL_SERVER` + `KVMCTL_TOKEN` env-var values. Do NOT
   proceed without a token.
3. All tools pass the token as `Authorization: Bearer <token>`.
4. The default homelab install uses a self-signed TLS cert. Pass
   `-k` to curl / `insecure: true` to the HTTP client. Surface a
   one-line banner the first time: "note: skipping TLS verification
   (self-signed cert)".

## Read-only vs destructive

Read-only tools (list/get) run freely. Destructive tools require the
operator to confirm in chat before the call:

- `vm_action` with action in {STOP, FORCESTOP, RESTART, POWER_CYCLE,
  MIGRATE, OFFLINE_MIGRATE, DELETE_VM, REVERT_SNAPSHOT, DELETE_SNAPSHOT}
- `create_snapshot` (non-destructive but burns storage; confirm if
  quota is tight)
- `force_fail_migration`

Confirmation script: "This will {action} VM {name} on {host}. Proceed?"
Wait for an explicit yes. One confirmation per call, not per batch.

## Tools

Full schemas are in TOOL.md; quick reference:

| Tool | HTTP | Notes |
|------|------|-------|
| `list_hosts` | GET /api/hosts | All hypervisors + status |
| `list_vms` | GET /api/vms[?host=...] | Flat or per-host VM list |
| `vm_detail` | GET /api/vms/{uuid} | Hardware + runtime detail |
| `vm_action` | POST /api/command | Lifecycle verbs |
| `list_migrations` | GET /api/migrations | Recent migrations |
| `list_snapshots` | GET /api/vms/snapshots?uuid=... | Snapshots for one VM |
| `create_snapshot` | POST /api/command (SNAPSHOT) | New internal snapshot |
| `system_health` | GET /api/admin/system-health | Green/amber/red rollup |
| `audit_search` | GET /api/audit/events?... | Recent audit events |
| `reconcile_host` | POST /api/admin/reconcile/{host} | Force telemetry refresh |

## Examples

**"Show me VMs on hvnode02 that aren't running"**
→ `list_vms` with `host=hvnode02`, filter `state != running` client-side,
print a short table.

**"Power-cycle VM1 and watch"**
→ Confirm. `vm_action` with `POWER_CYCLE`. Poll `list_migrations` +
`vm_detail` for state transitions until VM is running again.

**"Any hypervisors outdated?"**
→ `system_health`, report the `hypervisors` subsystem card. If it's
amber on outdated agents, call `list_hosts` to show which ones.

**"Take a pre-patch snapshot on every running Rocky VM"**
→ Confirm. `list_vms`, filter `guest_os ~ /Rocky/` and `state ==
running`, loop `create_snapshot` with `name=pre-patch-YYYYMMDD`.
Summarise count + any failures.

**"Migration stuck"**
→ `list_migrations status=running`. If one is older than 90 min,
suggest `force_fail_migration` with the operator's reason. Don't
auto-force-fail.

## Gotchas

- Mutating commands carry a `request_id` server-side for idempotency;
  retries don't double-execute. Do NOT worry about retry logic.
- SSE events (inventory, migration) would be useful but the skill
  polls instead — simpler, always-on.
- Admin-only tools (`system_health`, `audit_search`, `reconcile_host`)
  require the token to belong to a user with `user.manage`. On 403,
  tell the operator: "token lacks user.manage permission; regenerate
  from an admin session."
- Rate-limiting isn't enforced today but avoid tight loops; 5-10 RPS
  is fine, 100+ is abusive.
