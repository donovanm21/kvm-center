---
title: Backup and restore
section: Operations
order: 68
---

# Backup and restore

kvm-center ships a built-in backup for the control plane's state.
Physical VMs on the hypervisors are **not** affected by backup or
restore — those are handled by your libvirt / storage backup strategy.

## What's in a backup

A kvm-center backup tarball contains:

| File | Contents |
|------|----------|
| `manifest.json` | Version, timestamp, operator identity, schema format, bytes |
| `store.json` | The full `persistData` snapshot — users, groups, roles, API tokens, hosts, VM inventory, snapshot policies, audit events, features, notifications config, metrics config, hypervisor certs, console secret, command history, gateway pairing, retired hosts and pending enrolments |
| `tls/server.crt`, `tls/server.key` | Configured HTTPS certificate and key |
| `ca/*` | Cluster CA, grant signer and secret-box material |

Not included (out-of-band — back up separately):

- `.env` (contains `KVM_MONGO_PASSWORD`)
- MongoDB data volume (alternative to `store.json` for same-version restore)

## Take a backup

### From the API

`POST /api/admin/backup` (requires `user.manage`) streams a `tar.gz`:

```sh
curl -fsSLOJ \
  -H "Authorization: Bearer $KVMCTL_TOKEN" \
  -k https://kvm-center.example.com:8190/api/admin/backup
```

The response is saved as `kvm-center-backup-YYYYMMDD-HHMMSS.tar.gz`.

### From the docker host

The API export includes configured TLS and CA files and fails if
configured material cannot be read. For an additional disaster-recovery
copy, pair it with a filesystem copy of the server's data volume:

```sh
ssh <your-docker-host> '
  mkdir -p /tmp/kvmc-dr
  # 1. API export (catches the full store snapshot)
  curl -fsSLk -o /tmp/kvmc-dr/store.tar.gz \
    -H "Authorization: Bearer $KVMCTL_TOKEN" \
    https://localhost:8190/api/admin/backup
  # 2. TLS + CA material (on-disk)
  docker cp kvm-center-server:/var/lib/kvm-center/tls  /tmp/kvmc-dr/tls
  docker cp kvm-center-server:/var/lib/kvm-center/ca   /tmp/kvmc-dr/ca
  # 3. .env (contains KVM_MONGO_PASSWORD — keep safe)
  cp /opt/kvm-center/.env /tmp/kvmc-dr/env
  tar czf /tmp/kvmc-dr-$(date +%Y%m%d).tar.gz -C /tmp kvmc-dr
'
```

## Restore

Online restore is temporarily disabled. `POST /api/admin/restore`
returns **503** without reading the upload or changing live files.
A safe replacement must restore the active backend and security files
transactionally, with live writers stopped. The current API does not
provide a validation-only or dry-run mode.

### Offline recovery

Plan and rehearse recovery on an isolated deployment first. Preserve a
separate, verified copy of the existing database and security volumes
before replacing anything. Use a backup from a trusted source and verify
all manifest checksums and TLS/CA material before copying files.

The steps below apply only to a deployment using the **JSON backend**.
For MongoDB, see the backend-specific requirements below; copying
`data.json` does not replace an existing Mongo database.

1. Stop the stack: `docker compose -f /opt/kvm-center/docker-compose.yml stop kvm-center-server`
2. Extract the backup tarball and inspect `manifest.json` to confirm
   the version matches (major/minor at minimum).
3. Copy `store.json` into the server's data volume:
   ```
   docker run --rm -v kvm-center_kvm-center-data:/target \
     -v "$PWD:/src" alpine \
     cp /src/store.json /target/data.json
   ```
   (Replace `kvm-center_kvm-center-data` with `$(docker compose -f
   /opt/kvm-center/docker-compose.yml config --volumes | grep data)`
   if the compose project name differs.)
4. If restoring TLS + CA material, copy `tls/` and `ca/` into their
   respective volumes the same way.
5. Restart: `docker compose -f /opt/kvm-center/docker-compose.yml start kvm-center-server`
6. Confirm: `/api/version` returns, login works, the Hosts page
   shows the expected inventory.

## Mongo notes

When MongoDB is configured, it is the authoritative persistent store.
The API backup serializes the in-memory state loaded from that backend,
including storage-only credential fields, but restoring `data.json`
alone does **not** replace a populated Mongo database. It is not a live
parallel cache.

Use a tested MongoDB-native restore plan, or import the snapshot into a
separate fresh recovery database and verify it before changing the server's
configuration. Keep the original database intact until recovery is
validated. The legacy bootstrap restore helper does not provide an atomic
Mongo restore and must not be treated as a one-step recovery workflow.

For a Mongo-level point-in-time backup (e.g. against a
filesystem-corruption scenario), use mongodump:

```sh
docker compose -f /opt/kvm-center/docker-compose.yml exec kvm-center-mongo \
  mongodump --uri "mongodb://kvmc:$KVM_MONGO_PASSWORD@localhost:27017/kvm-center" \
  --archive=/tmp/kvmc.archive --gzip
docker compose -f /opt/kvm-center/docker-compose.yml cp \
  kvm-center-mongo:/tmp/kvmc.archive ./kvmc-mongo-$(date +%Y%m%d).archive
```

Pair with the `/api/admin/backup` export and you cover both the
"app state corrupt" and "DB-volume corrupt" scenarios.

## Safety during active migrations

Both scheduled and ad-hoc backups serialize the in-memory store
under a read lock — they are safe to run concurrently with a
migration. The migration's progress frames update the
MigrationRecord under a write lock that blocks the export
momentarily; the export resumes once the writer releases. Worst
case: the export takes a few seconds longer than usual.

Online restore remains disabled even when no migrations are running.
Offline recovery requires stopping the service and accounting for any
in-flight hypervisor operations before replacing control-plane state.

## Rollback

Use the verified database and security-volume copies taken before recovery.
Do not assume a migration artefact such as `data.json.migrated` contains
the latest state. Validate login, pairing, inventory and command history in
an isolated recovery deployment before resuming normal operations.

## Scheduling

**Settings → Backup → Schedule** (v5.43.0+). Tick the **Enable**
checkbox, set how often (`every_hours`), pick a destination path
on the docker host, and choose how many tarballs to keep
(`retain_count`). The server runs a once-per-minute scheduler tick
that fires when `time.Since(last_run) >= every_hours`. Tarballs
are written atomically (`.tmp` + rename) and rotation drops the
oldest beyond `retain_count`.

The scheduler writes to a host directory that's mounted into the
container — pick a path under a bind-mounted volume so backups
survive container rebuilds. The default `/var/lib/kvm-center/backups`
follows that pattern in the shipped compose stack.

Disabled by default — opt in when ready. Outcome (last-run
timestamp, size, error) is exposed in the UI so a stuck scheduler
is visible at a glance. Want a different cadence than a fixed
`every_hours`? File an issue — we can add a real cron expression
if there's demand.
