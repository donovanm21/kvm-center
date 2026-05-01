---
title: Operations
section: Reference
order: 70
---

# Operations

Reference for the v5.48 bullet-proof async-command substrate. If
something looks wrong with a long-running command — stuck pending,
mysteriously cancelled, etc. — start here.

## ops.db — the agent's persistent operation store

Every command the agent executes leaves a row in
`/var/lib/kvm-center/operations.db` (overrideable per-host via
the `KVM_AGENT_OPS_DB` env var). This is a SQLite database; no
ceremony required to inspect it.

Schema:

```sql
CREATE TABLE operations (
  request_id        TEXT PRIMARY KEY,
  action            TEXT NOT NULL,
  vm_uuid           TEXT,
  vm_name           TEXT,
  params_json       TEXT,
  state             TEXT NOT NULL,    -- pending|running|success|failed|cancelled|unknown
  submitted_at      INTEGER NOT NULL, -- unix ns
  started_at        INTEGER,
  last_heartbeat_at INTEGER,
  completed_at      INTEGER,
  output            TEXT,
  error             TEXT,
  child_pid         INTEGER,
  watchdog_at       INTEGER
);
```

Useful one-liners:

```sh
# Most recent terminal outcomes
sqlite3 /var/lib/kvm-center/operations.db \
  "select request_id, action, state, vm_name, error
   from operations
   where state in ('failed','cancelled','unknown')
   order by submitted_at desc limit 20"

# Anything still running right now
sqlite3 /var/lib/kvm-center/operations.db \
  "select request_id, action, vm_name,
          (strftime('%s','now')*1e9 - submitted_at)/1e9 || 's old'
   from operations where state='running'"

# How long actions have been taking
sqlite3 /var/lib/kvm-center/operations.db \
  "select action,
          count(*),
          avg((completed_at-started_at)/1e9) || 's avg'
   from operations where state='success' and started_at is not null
   group by action order by action"
```

Retention: 7 days for terminal rows (success/failed/cancelled/
unknown). Running/pending rows are kept indefinitely until they
reach terminal — a stuck row is data, not noise. GC runs hourly.

## Watchdog matrix

Every command runs under a per-action watchdog. On timeout the
agent SIGTERMs the child, waits 5 s, SIGKILLs. Override per-call
via the request body's `params.watchdog_seconds` (integer, seconds).

| Action | Cap |
|---|---|
| `START`, `STOP`, `RESTART`, `FORCESTOP` | 60 s |
| `POWER_CYCLE` | 90 s |
| `SNAPSHOT`, `REVERT_SNAPSHOT` | 5 min |
| `DELETE_SNAPSHOT` | 30 min |
| `MIGRATE` (live) | 60 min |
| `OFFLINE_MIGRATE` | 90 min |
| `EDIT_VM` (no shutdown) | 30 s |
| `EDIT_VM` (`allow_shutdown=true`) | 5 min |
| `CHANGE_MEDIA`, `DELETE_VM` | 30–60 s |
| anything else | 60 s |

When the watchdog fires the ops.db row is marked `failed` with
`error="watchdog: <action> did not complete within Ns"`.

## Boot reconciliation

On agent startup, every row left in `running` state from a prior
process is resolved by querying libvirt:

| Action | Resolution |
|---|---|
| `START` | running → success; else failed |
| `STOP` / `FORCESTOP` | running → failed; stopped/missing → success |
| `DELETE_VM` | missing → success; else unknown |
| `SNAPSHOT` | snapshot present → success; else failed |
| `DELETE_SNAPSHOT` | snapshot gone → success; else still-present → failed |
| anything else | unknown — manual review |

If the recorded `child_pid` is still alive in `/proc` at startup
(rare: usually `Pdeathsig=SIGKILL` kills the child with the agent),
the row is marked `unknown` with the PID for the operator's review.

## Async dispatch

Set `KVM_AGENT_ASYNC_COMMANDS=true` in the agent's environment
(systemd drop-in is convenient) to opt into 202-Accepted dispatch
for slow actions. Eligible: `DELETE_SNAPSHOT`, `OFFLINE_MIGRATE`,
`DELETE_VM`, `POWER_CYCLE`. Live `MIGRATE` is intentionally
synchronous-only until its migration-record lifecycle is async-
aware.

## Cancelling a stuck action

```sh
# By request_id — find it in the SPA's command-log or admin UI:
curl -k -X POST -d '{"reason":"taking too long"}' \
  -H 'content-type: application/json' \
  https://<server>:8190/api/command/cancel?request_id=req_abc
```

The server proxies to the agent, the agent SIGTERMs the child,
waits 5 s, SIGKILLs. For `MIGRATE`/`OFFLINE_MIGRATE` it also
invokes `virsh domjobabort` so libvirtd cleans up its state
machine. The ops.db row is marked `cancelled` with the operator's
reason; the SPA's command-log reflects it within seconds via the
SSE `command` event.

Cancellation is idempotent — cancelling an already-terminal row is
a no-op that returns the existing state.
