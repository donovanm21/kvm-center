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
| `store.json` | The full `persistData` snapshot — users, groups, roles, API tokens, hosts, VM inventory, snapshot policies, audit events, features, notifications config, metrics config, hypervisor certs, console secret |

Not included (out-of-band — back up separately):

- TLS server cert + key at `$KVM_CENTER_TLS_DIR`
- Cluster CA material at `$KVM_CENTER_CA_DIR`
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

For a full disaster-recovery backup that includes the TLS + CA
material, pair the API export with a filesystem copy of the server's
data volume:

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

There are two ways to restore. Both write the staged files into the
live volumes and require a server restart to load the new state —
the difference is whether you do that from the SPA or the host.

### From the SPA (v5.39.0+)

**Settings → Backup → Restore.** Pick the backup `.tar.gz`, type
`RESTORE` to confirm, click **Apply restore**. Checksums in
`manifest.json` are verified before any file is touched. After the
upload succeeds the **Restart server** button enables — clicking it
saves any pending state, exits the process, and lets the container
restart policy bring it back against the restored data.

Requires `user.manage`. Audit events: `backup.restore` for the
upload, then again for the restart.

### From the docker host (CLI)

Same effect, useful when the SPA isn't reachable (DR scenarios where
the cluster is being rebuilt from scratch).

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

## Rollback

If restore goes wrong, the previous `store.json` is still in Mongo's
`data.json.migrated` (v5.1+ migration artefact) or in the pre-restore
volume snapshot you should have taken. Roll back by reversing step 3.

## Scheduling

The v5.27 backup endpoint is unscheduled — operators run it ad-hoc or
wrap it in their existing backup tooling (Borg, Restic, Kopia, cron).
A future patch may add a Settings → Backup tab with a cadence + retention
policy if operators ask for it.
