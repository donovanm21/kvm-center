---
title: Migrations
section: Reference
order: 50
---

# Migrations

kvm-center supports live and offline migration between any two
enrolled hypervisors. Live migrations stream the running VM state
over mTLS; offline migrations ship a compressed disk then define +
start on the target.

An offline migration from the UI, with the progress bar running:

![Offline migration from the Actions menu](/api/docs/media/03-live-migration.webp)

## Transports

| Transport | When used | How the bytes move |
|-----------|-----------|--------------------|
| **mTLS** | Both source + dest have an active cluster-CA cert | Live: `virsh migrate qemu+tls://<dest>/system`. Offline: `virsh -c qemu+tls://<dest>/system vol-upload`. |
| **SSH** (legacy) | Either side isn't enrolled yet, or operator pinned it | Live: `qemu+ssh://kvm-migrator@<dest>/system` via the shared keypair. Offline: `scp` + `virsh define` via the same user. |

The Migrate modal surfaces a Transport toggle (**Auto / mTLS
(Recommended) / SSH (Legacy)**). Auto picks mTLS when both hosts are
ready. The migration record's **Transport** column reflects what
actually ran.

## Grants (v5.11+)

Every mTLS migration carries an Ed25519-signed grant bound to
`{src, dst, vm_uuid, 5-min TTL}`. Flow:

1. Server mints + records the grant (`migration.grant.issue` audit).
2. Source agent forwards it to the destination's `/prepare-grant`
   before any libvirt call.
3. Destination validates offline against the cached server pub key,
   checks binding + TTL, records single-use in a durable log.
4. `/etc/libvirt/hooks/qemu` dials the local agent during "prepare
   begin"; no cached grant ⇒ libvirtd refuses the migration.

This prevents an operator running `virsh migrate` by hand on a
hypervisor from bypassing kvm-center's authorization layer.

## Advanced offline options

Offline-only, under the Migrate modal's **Advanced** section:

- **Target storage pool** — pick from pools discovered via telemetry
  on the destination. Defaults to "default" if present, else the
  first running pool. Used for `virsh vol-create-as` + `vol-upload`.
- **Rename VM on destination** — rewrites `<name>` in the domain
  XML. UUID preserved so per-VM state (notes, guest-telemetry
  cache, console secret) follows the migration.
- **Regenerate MAC addresses** — emits fresh 52:54:00-prefixed
  locally-administered MACs per interface. Use when cloning so the
  source VM keeps its network identity.

Live migrations don't expose these — live assumes identical config
on both sides.

## SSH-transport host-key checking (v5.44.0)

When the SSH transport is in use the agent dials with
`StrictHostKeyChecking=no` by default — fine on a dedicated
management VLAN, but flagged as a hardening item in the v5.32 review.

**Settings → General → Strict SSH host-key checking.** Flip the
toggle on to make agents dial with `StrictHostKeyChecking=yes`.
Operators must populate `/var/lib/kvm-migrator/.ssh/known_hosts`
on every agent **before** flipping the flag — otherwise SSH
migrations refuse with "host key verification failed" on the first
unrecognized destination.

The setting threads through `Features.SSHStrictHostKeyChecking` →
`/api/command` params (`strict_host_key=yes`) → agent's
`setSSHStrict` → `ssh -o StrictHostKeyChecking=…`.

Audit events for SSH migrations have carried the chosen transport
since v5.37.1, so the trail still distinguishes legacy SSH dials
from the strict ones.

## Concurrency

The server refuses overlapping migrations on the same source or
destination host with a 409 response. Live migrations are
synchronous on the agent; offline runs async and the server polls
`/migrate-status` every second.

## Cancellation

The Migrations view's Cancel button issues a graceful abort:

- Live: `virsh domjobabort <vm>` on the source.
- Offline: the agent honours a context cancel at every step and
  rolls back (deletes transferred disks, restarts the source VM if
  we'd already shut it down).

## Troubleshooting

See [Troubleshooting → Migrations](/docs/troubleshooting#migrations)
for the common failure modes and their fixes.
