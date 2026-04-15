---
title: Networking & firewall
section: Operations
order: 65
---

# Networking & firewall

Everything kvm-center does is HTTP(S) or libvirt-over-TLS. This
page lists every port it listens on or dials, and what to budget
for bandwidth during migrations.

## Listening ports

### Control plane (server host)

| Port  | Protocol | Consumer             | Scope            | Notes |
|-------|----------|----------------------|------------------|-------|
| 8190  | HTTPS    | Operators + kvmctl   | LAN / trusted    | Primary UI + API. Terminate behind a reverse proxy for WAN exposure. |
| 8090  | HTTP     | Agents + install one-liners | Management VLAN | Plaintext API + `/downloads/*` for agent / kvm-tools / kvmctl binaries. Agents trust this during bootstrap before they've pinned the server cert. |
| 8096  | HTTP     | Legacy SPA (nginx)   | localhost        | Kept for `/legacy/` access; retire once no one browses it. |

### Hypervisors

| Port         | Protocol     | Who dials it           | Notes |
|--------------|--------------|------------------------|-------|
| 8081         | HTTPS        | Control plane          | Agent command + telemetry channel. |
| 16514        | TCP (TLS)    | Other hypervisors      | libvirt mTLS control (migrations). |
| 49152-49215  | TCP          | Other hypervisors      | QEMU migration data range. |
| 5900-59xx    | TCP          | Control plane (VNC proxy) | Per-VM VNC sockets, usually bound to localhost — kvm-center proxies them over 8081 so you don't expose them. |

### Guests (kvm-tools)

Outbound only. Guests post telemetry to the control plane on port
**8190** (TLS) or **8090** (plaintext) — whichever the operator
configured at install time. Nothing listens inside the guest.

## Firewall recipe

`install-agent.sh` (v5.10.8+) opens the hypervisor-side ports via
firewalld automatically. For iptables / nftables-only deployments:

```sh
# hypervisor
firewall-cmd --permanent --add-port=8081/tcp
firewall-cmd --permanent --add-port=16514/tcp
firewall-cmd --permanent --add-port=49152-49215/tcp
firewall-cmd --reload

# control plane
firewall-cmd --permanent --add-port=8190/tcp
firewall-cmd --permanent --add-port=8090/tcp
firewall-cmd --reload
```

Traffic directions to permit between nodes:

- **Control plane → hypervisors**: 8081/tcp.
- **Hypervisors → control plane**: 8190/tcp (telemetry + CA enroll)
  and 8090/tcp (binary pulls during `update-agent.sh`, via
  `/downloads/*`).
- **Hypervisor ↔ hypervisor**: 16514/tcp and 49152-49215/tcp.
  East-west only — never exposed beyond the management VLAN.
- **Operators → control plane**: 8190/tcp. If users hit the UI via
  a reverse proxy, that's the only inbound port the server needs.

## Bandwidth considerations

### Live migration

Live migration streams guest RAM + dirty pages until the VM
converges. Budget on the migration data range (49152-49215/tcp):

| Guest RAM | Idle guest | Write-heavy guest |
|-----------|------------|-------------------|
| 2 GB      | ~20 s @ 1 Gbps | ~45 s, converges |
| 8 GB      | ~75 s @ 1 Gbps | may stall w/o 10 GbE |
| 32 GB     | ~5 min @ 1 Gbps | plan for 10 GbE |

**1 GbE is the practical floor** for live migration. Anything
larger than 16 GB RAM or actively writing to memory wants 10 GbE
between hypervisors or it will fail to converge and fall back to
paused-page-copy, which looks like a long hang to the guest.

Set `copy_storage=true` only when the destination doesn't share the
source's storage pool — it doubles the data transferred (disk +
RAM). A 50 GB qcow2 on 1 GbE is ~7 minutes by itself.

### Offline migration

Offline copies the whole disk. No RAM transfer, so throughput is
the bottleneck and there's no convergence problem.

- **mTLS path** (v5.11.6+, default): `virsh vol-upload` over
  qemu+tls://. Streams the raw disk — budget `disk_size /
  link_speed`. A 50 GB disk on 1 GbE is ~7 minutes; on 10 GbE,
  ~45 seconds.
- **SSH path** (legacy fallback): scp over OpenSSH. Slightly slower
  due to double-encryption (SSH + any qcow2 compression). Same
  order of magnitude.

### Telemetry + metrics

Negligible. Each agent posts a ~5 KB JSON payload every 10 s
(~500 B/s steady-state per hypervisor). InfluxDB line-protocol
writes from the server are similar. None of this shows up on a
bandwidth graph.

## Latency

Migrations tolerate up to ~5 ms RTT comfortably, ~20 ms with
longer convergence windows. The control plane → agent channel is
request/response and doesn't care about latency beyond "does the
browser feel laggy."

Cross-site migration works but is not recommended unless both
sites share an L2 stretched network for the guest's bridge —
libvirt does not retag VLANs for you, and the guest's MAC + IP
follow it across.

## Hardening checklist

- Expose **8190** to operators. 8090 (agents + download one-liners)
  stays management-VLAN-only; 8096 stays localhost.
- Put the server behind a reverse proxy if operators come in over
  WAN. HSTS + a real cert beats the self-signed bundle.
- Lock 16514 + the QEMU range to the hypervisor subnet.
- Guests never need inbound rules for kvm-tools.
- Rotate the cluster CA (regen via Settings → TLS → CA) if you
  suspect compromise — all hypervisor leaf certs need to be
  re-enrolled afterwards.
