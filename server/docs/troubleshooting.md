---
title: Troubleshooting
section: Reference
order: 80
---

# Troubleshooting

## Migrations

| Symptom | Fix |
|---------|-----|
| `No route to host` connecting to 16514 | Firewall. `firewall-cmd --permanent --add-port=16514/tcp --add-port=49152-49215/tcp && firewall-cmd --reload` on both source + dest. |
| `authentication failed: Failed to verify peer's certificate` | Certs signed by different CAs (partial re-enrollment). Re-run `install-agent.sh --enroll-token` on both sides. |
| `Certificate owner does not match the hostname` | Connecting by IP/FQDN not in the cert's SAN list. Re-enroll with the FQDN you actually dial. |
| Ghost VM on source after live migration | Pre-v5.10.4 agent. Upgrade via `bootstrap.sh --update-agents`. |
| `dest open(...): Permission denied` on offline scp | Existing disk on target is `qemu:qemu 0600`. Fixed in v5.11.2 — upgrade. |
| Out-of-band `virsh migrate` refused by libvirtd | Expected — the qemu hook on dest requires an active server-issued grant. Use the kvm-center SPA or `kvmctl vm migrate`. |

## Enrollment

| Symptom | Fix |
|---------|-----|
| `enrollment token invalid or expired` | Tokens are one-time and live 10 min in memory. Mint a fresh one. |
| Hypervisor doesn't show the mTLS pill | Cert exists but `hypervisor_certs.<hostid>` Host ID doesn't match. Check that you enrolled with the FQDN that matches the `_id` in `db.hosts`. |
| Binary download fails from a hypervisor | v5.24+ serves binaries at `https://<server>:8190/downloads/*` — install snippets pass `curl -k` to tolerate the default self-signed cert. Make sure 8190/tcp is reachable from the hypervisor's management VLAN. |

## Auth

| Symptom | Fix |
|---------|-----|
| `invalid or expired token` on kvmctl | Token may be revoked or expired. `kvmctl logout` then re-login with a fresh token. Personal tokens default to 30 days. |
| Lost admin access | Use a system token with `admin` permission (still valid even if the admin password is lost). If none exists, run the server with `--recovery-admin` (v5.15+) or restore from backup. |

## Metrics

| Symptom | Fix |
|---------|-----|
| `influx 401` on Send test sample | Token doesn't belong to the bucket's org. Rebuild the Influx token with write access to the bucket. |
| No data in Grafana after enabling | Confirm Settings → Metrics shows a non-zero last-send count within 30 s. Verify bucket + measurement prefix match the dashboard variables. |

## Commands

| Symptom | Fix |
|---------|-----|
| Action returns `failed` with `error: watchdog: <action> did not complete within Ns` (v5.48+) | The action's per-action watchdog killed the underlying virsh / qemu-img process. Look at the matrix below before assuming a bug — large VMs sometimes need a manual override. |
| Long action shows as `pending` in command-log for several minutes (v5.47.2+) | Expected. The server's POST /command timed out (or got a 5xx) so we don't yet know whether the agent finished. The reconciler polls each agent every 30 s and resolves the entry once the agent reports back. The deadline before pending → `unknown` is 30 minutes. |
| Same long action lands as `failed` in admin UI but the VM is in the expected state (pre-5.47.2) | Known bug pre-v5.47.2: HTTP timeouts to the agent locked in a `failed` verdict even when libvirt completed the action. Upgrade. |
| Pending command never resolves (v5.47.2+) | Check the agent is reachable on `:8081` from the server (`curl -k https://<host>:8081/health`). The reconciler logs `command-reconciler: poll …: <err>` every 30 s on connection failure. If the agent is reachable, the request_id may have aged out of its 35-min outcome cache — entry will flip to `unknown` after 30 min. |
| Admin UI shows `command.reconciled` audit events flooding | Symptom of an agent in a permanent error loop returning 5xx for every command. Check the agent log on the host. |
| `409 Conflict — another action is already in flight for this target` (v5.48.1+) | Expected; one of the operator's tabs already has a pending action on this VM. The response carries the `conflicting_request_id` — find it in the Recent Tasks pane. Cancel it first if you want to override. |

### Boot reconciliation (v5.48+)

When the agent restarts, any action that was in flight is in
ops.db with `state=running`. The agent resolves these on startup
by querying libvirt:

| Action | Resolution rule |
|---|---|
| START | `virsh domstate` running → success; else failed. |
| STOP / FORCESTOP | running → failed; stopped/missing → success. |
| DELETE_VM | VM missing → success; else unknown. |
| SNAPSHOT | snapshot present → success; else failed. |
| DELETE_SNAPSHOT | snapshot gone → success; else failed. |
| MIGRATE / OFFLINE_MIGRATE / EDIT_VM / CHANGE_MEDIA / REVERT_SNAPSHOT / POWER_CYCLE | unknown — manual review (libvirt's instantaneous state can't tell us whether the action applied). |

If the recorded `child_pid` is still alive in `/proc` at startup
(rare; usually the parent's `Pdeathsig` SIGKILL takes the child
with it), the row is marked `unknown` with the PID in the error
field. The action may complete on its own; the operator decides.

### Per-action watchdog matrix (v5.48+)

The agent kills any virsh / qemu-img child that runs longer than
the cap below, marks the operation `failed` with
`error="watchdog: …"`, and clears the child PID. Override per-call
via `params.watchdog_seconds=<int>`.

| Action | Cap | Notes |
|---|---|---|
| `START`, `STOP`, `RESTART`, `FORCESTOP` | 60 s | Catches a wedged libvirtd. |
| `POWER_CYCLE` | 90 s | Shutdown + start; ACPI shutdown can take ~30 s. |
| `SNAPSHOT` | 5 min | qcow2 snapshot is metadata-only. |
| `DELETE_SNAPSHOT` | 30 min | qemu-img commit can be slow on big VMs. |
| `REVERT_SNAPSHOT` | 5 min | Rare slow case. |
| `MIGRATE` (live) | 60 min | Includes convergence loop. |
| `OFFLINE_MIGRATE` | 90 min | Disk transfer; huge VMs need headroom. |
| `EDIT_VM` (no shutdown) | 30 s | Pure XML edit. |
| `EDIT_VM` (`allow_shutdown=true`) | 5 min | Includes ACPI shutdown + restart. |
| `CHANGE_MEDIA`, `DELETE_VM` | 30–60 s | |
| anything else | 60 s | Default fallback. |

## Notifications

| Symptom | Fix |
|---------|-----|
| Discord rejects with 400 | Pre-v5.4.2 agent shipped raw event JSON. Upgrade — the auto-detect maps Discord URLs to the chat envelope. |
| Webhook never fires on migration | Pre-v5.4.3 pointer-aliasing bug — upgrade. Also check Settings → Notifications → event subscriptions include the relevant type. |

## Runbooks

### Patching a hypervisor — entering maintenance mode

Use maintenance mode whenever you're about to reboot, kernel-patch,
or hardware-touch a hypervisor. Workflow:

1. **Hosts → click the host → Maintenance**. Optional: tick
   *Drain VMs* to live-migrate every running guest off before
   the host enters maintenance.
2. The host reports `maintenance=true` to the placement gate. New
   VM creation targeting it is refused, and any `MIGRATE` whose
   destination is the host is refused. Migrations *off* the host
   are still allowed — that's the point of draining.
3. Watch the Migrations view. Drain runs sequentially (one VM at
   a time) so the existing concurrent-migration gate stays clean;
   each VM picks the least-loaded online host as its target.
4. Once the host is empty / patched / rebooted, **Maintenance →
   Exit**. The host returns to the placement pool on its next
   telemetry tick (≤ 30 s).

What if a drain VM has no eligible target? It's skipped (the
audit row says `skipped: no eligible target`) and the drain
continues to the next VM. Non-draining patches still work — exit
maintenance after the reboot and the host catches back up.

### Hypervisor agent offline

The dashboard marks a host **OFFLINE** after 90 s without a
telemetry tick (the host-mark loop runs every 30 s). Effects:

- Inventory data goes stale but is preserved — VM rows linger so
  the operator still sees what *was* there.
- Commands targeting the host (start, stop, snapshot, console)
  return *agent unreachable* immediately rather than blocking.
- Migrations whose source is the offline host **fail** and are
  marked terminal so they don't sit in `running` forever.

Recovery checklist:

1. SSH to the hypervisor, `systemctl status kvm-center-agent`. If
   the unit is running, the issue is network — confirm
   `curl -k https://<server>:8190/health` responds.
2. If the unit is stopped, `journalctl -u kvm-center-agent -n 100`
   usually names the cause (cert expired, libvirtd offline,
   binary segv).
3. **Cert expired** is by far the most common: re-run the install
   one-liner with a fresh enrollment token from Settings → TLS →
   Hypervisor PKI. The new cert replaces the old in place; the
   agent restarts cleanly.

The "phantom prune" sweep removes hosts with no telemetry for >
24 h *and* zero VMs — it never deletes a host whose VM rows are
still being reported by another path. So a stale-but-lived-in
host stays visible until you explicitly remove it.

### Resetting a forgotten admin password

Three escape hatches in priority order:

1. **System token** — any user with `admin` on a system token can
   reset another user's password via Settings → Users → Edit →
   Reset password. Tokens never carry TOTP, so this works even if
   the human admin lost their authenticator.
2. **`--recovery-admin` flag** (v5.15+) — start the server once
   with this flag set and an `admin` / `recovery` account is
   created (or its password reset, if it already exists) at boot.
   Log in, change the password, and remove the flag. Audit row is
   written so the action is visible afterwards.
3. **Restore from backup** — last resort if you don't have a
   system token *and* can't restart the server with new flags.
   See `backup.md` for the import path; the restored snapshot
   carries the admin user's old password hash so you'd need a
   backup taken when the password was known.

## Database

| Symptom | Fix |
|---------|-----|
| `mongo connection refused` at boot | The `kvm-center-mongo` container exited or didn't reach Ready. `docker compose logs kvm-center-mongo` — disk-full and OOM are the usual suspects. |
| `mongo exit 14` after a fresh install | Disk pressure on the docker host. Free space before the next start; the `feedback_async_save.md` memory has the long story. |
| Slow startup, telemetry stalls | Mongo working-set is too big for available RAM. Default deploys are sized for ~50 hosts; bump container memory or migrate to a dedicated DB host. |
