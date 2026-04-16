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

## Notifications

| Symptom | Fix |
|---------|-----|
| Discord rejects with 400 | Pre-v5.4.2 agent shipped raw event JSON. Upgrade — the auto-detect maps Discord URLs to the chat envelope. |
| Webhook never fires on migration | Pre-v5.4.3 pointer-aliasing bug — upgrade. Also check Settings → Notifications → event subscriptions include the relevant type. |
