# Agent Guide

The KVM Center agent (`kvm-center-agent`) is a single static Go binary that runs as a systemd service on each KVM hypervisor. It collects host and VM metrics, reports them to the server, and executes commands on demand.

---

## How It Works

The agent runs two concurrent goroutines:

```
┌──────────────────────────────────────────────────────┐
│                  kvm-center-agent                    │
│                                                      │
│  ┌───────────────────────┐  ┌──────────────────────┐ │
│  │    Telemetry Loop     │  │   Command Server     │ │
│  │                       │  │                      │ │
│  │  every 30 seconds:    │  │  POST /command       │ │
│  │  1. Read /proc/stat   │  │  POST /health        │ │
│  │  2. Read /proc/meminfo│  │                      │ │
│  │  3. virsh list --all  │  │  Executes virsh and  │ │
│  │  4. virsh dominfo ...  │  │  returns result      │ │
│  │  5. virsh dumpxml ... │  │                      │ │
│  │  6. virsh domstats .. │  │  Listens on :8081    │ │
│  │  7. virsh pool-list   │  │                      │ │
│  │  POST /api/telemetry  │  └──────────────────────┘ │
│  └───────────────────────┘                           │
└──────────────────────────────────────────────────────┘
```

---

## Metrics Collected

### Host Metrics

| Metric | Source | Notes |
|---|---|---|
| `cpu_usage` | `/proc/stat` | Delta between two readings; 0% on first report |
| `cpu_count` | `runtime.NumCPU()` | Logical cores |
| `mem_total_mb` | `/proc/meminfo MemTotal` | Total RAM in MB |
| `mem_free_mb` | `/proc/meminfo MemAvailable` | Available RAM (includes cache/buffers) |
| `mem_usage` | Computed | `(total - available) / total * 100` |
| `os` | `/etc/os-release PRETTY_NAME` | e.g. `Rocky Linux 9.7 (Blue Onyx)` |
| `kernel_ver` | `/proc/version` | e.g. `5.14.0-427.13.1.el9_4.x86_64` |
| `libvirt_ver` | `virsh --version` | e.g. `10.0.0` |
| `uptime` | `/proc/uptime` | Human-readable: `5d 3h 12m` |

### VM Metrics

For each VM discovered by `virsh list --all`:

| Metric | Source |
|---|---|
| `uuid` | `virsh dumpxml` → `<uuid>` element |
| `name` | `virsh list --all` |
| `state` | `virsh list --all` → mapped to `running/stopped/paused/crashed/unknown` |
| `cpu_count` | `virsh dominfo` → `CPU(s)` |
| `cpu_usage` | `virsh domstats --cpu-total` → `cpu.time` nanosecond delta |
| `mem_total_mb` | `virsh dominfo` → `Max memory` (KiB → MB) |
| `mem_usage_mb` | `virsh dominfo` → `Used memory` (KiB → MB) |
| `disk_path` | `virsh dumpxml` → `<disk><source file=...>` (first disk only) |
| `disk_usage_gb` | `syscall.Statfs` on the disk path directory |
| `disk_total_gb` | `syscall.Statfs` on the disk path directory |
| `networks[].mac` | `virsh dumpxml` → `<interface><mac address=...>` |
| `networks[].bridge` | `virsh dumpxml` → `<interface><source bridge=...>` |
| `networks[].rx_bytes` | `virsh domifstat <iface>` → `rx_bytes` |
| `networks[].tx_bytes` | `virsh domifstat <iface>` → `tx_bytes` |

### Storage Pool Metrics

For each pool from `virsh pool-list --all`:

| Metric | Source |
|---|---|
| `name` | `virsh pool-list` |
| `state` | `virsh pool-info` → `State` |
| `pool_type` | `virsh pool-info` → `Type` |
| `capacity_gb` | `virsh pool-info` → `Capacity` (with unit conversion) |
| `allocation_gb` | `virsh pool-info` → `Allocation` |
| `available_gb` | `virsh pool-info` → `Available` |
| `path` | `virsh pool-dumpxml` → `<target><path>` |

---

## VM CPU Usage Calculation

CPU usage is calculated from the libvirt `cpu.time` counter (nanoseconds of CPU time consumed since the VM was created). The agent stores the previous reading in memory and computes:

```
cpu% = (cpu_time_delta_ns / (interval_seconds × cpu_count × 1,000,000,000)) × 100
```

This means:
- The **first telemetry report always shows 0%** for VM CPU (no previous reading)
- If the agent restarts, the first report after restart also shows 0%
- Values are capped at 100% and rounded to 2 decimal places

---

## Commands

The agent's HTTP server handles `POST /command` with this body:

```json
{
  "action": "START",
  "vm_name": "MyVM",
  "vm_uuid": "d7c0b9e4-1234-5678-abcd-ef0123456789",
  "params": {}
}
```

The `vm_name` is preferred over `vm_uuid` for virsh calls (more reliable with some libvirt versions). If `vm_name` is empty, `vm_uuid` is used as the target.

### Supported Actions

| Action | virsh Command | Notes |
|---|---|---|
| `START` | `virsh start <vm>` | Powers on a stopped VM |
| `STOP` | `virsh shutdown <vm>` | Sends ACPI shutdown signal (graceful) |
| `FORCESTOP` | `virsh destroy <vm>` | Hard power off (immediate) |
| `RESTART` | `virsh reboot <vm>` | Sends reboot to running VM |
| `SNAPSHOT` | `virsh snapshot-create-as <vm> <name>` | `name` comes from `params.name` |
| `MIGRATE` | `virsh migrate --live --persistent <vm> qemu+ssh://<dest>/system` | `destination` comes from `params.destination` |

### Snapshot Naming

If `params.name` is not set, the agent generates a name: `snap-<unix_timestamp>`.

### Migration Requirements

Live migration requires:
- Passwordless SSH from the source hypervisor to the destination
- Shared storage (NFS or similar) accessible from both hosts, or block-level migration support
- The destination hypervisor in the same libvirt network

---

## Logging

The agent logs to the file configured in the systemd unit (`/var/log/kvm-center-agent.log`) and to the journal.

**View live logs:**
```bash
journalctl -u kvm-center-agent -f

# Or the log file directly:
tail -f /var/log/kvm-center-agent.log
```

**Typical output (normal operation):**
```
2026/04/11 21:16:01 kvm-center agent  server=http://10.10.100.8:8090  interval=30s  cmdPort=8081
2026/04/11 21:16:01 agent command server listening on :8081
```

Telemetry sends are silent on success. Failures are logged:
```
2026/04/11 21:16:31 telemetry: send failed: Post "http://10.10.100.8:8090/api/telemetry": ...
```

Command executions are logged:
```
2026/04/11 21:20:05 command: action=STOP vm=MyVM
2026/04/11 21:20:06 command ok: STOP vm=MyVM: Domain MyVM is being shut down
```

---

## Service Management

```bash
# Status
systemctl status kvm-center-agent

# Start / stop / restart
systemctl start kvm-center-agent
systemctl stop kvm-center-agent
systemctl restart kvm-center-agent

# Enable / disable autostart
systemctl enable kvm-center-agent
systemctl disable kvm-center-agent

# Live logs
journalctl -u kvm-center-agent -f

# Logs since last boot
journalctl -u kvm-center-agent -b
```

---

## Systemd Unit Reference

The installed unit at `/etc/systemd/system/kvm-center-agent.service`:

```ini
[Unit]
Description=KVM Center Agent
After=network.target libvirtd.service
Wants=libvirtd.service

[Service]
Type=simple
ExecStart=/opt/kvm-center/kvm-center-agent \
  -server http://10.10.100.8:8090 \
  -port 8081 \
  -interval 30s
Restart=always
RestartSec=10
StandardOutput=append:/var/log/kvm-center-agent.log
StandardError=append:/var/log/kvm-center-agent.log
SupplementaryGroups=libvirt kvm

[Install]
WantedBy=multi-user.target
```

To change any parameter (e.g. interval), edit the unit and reload:

```bash
systemctl edit kvm-center-agent --full
# Make changes, save
systemctl daemon-reload
systemctl restart kvm-center-agent
```

---

## Permissions

The agent runs as `root` with supplementary groups `libvirt` and `kvm`. This is required because:

- `virsh` commands need access to the libvirt socket at `/var/run/libvirt/libvirt.sock`
- Reading disk usage via `syscall.Statfs` on VM image paths may need elevated permissions
- The log file is at `/var/log/kvm-center-agent.log` (root-owned)

For stricter deployments, you could run as a dedicated user if that user is in the `libvirt` group and has read access to `/var/lib/libvirt/`.

---

## Upgrading the Agent

```bash
# On the hypervisor
systemctl stop kvm-center-agent
cp /tmp/kvm-center-agent-amd64 /opt/kvm-center/kvm-center-agent
chmod +x /opt/kvm-center/kvm-center-agent
systemctl start kvm-center-agent
systemctl status kvm-center-agent
```

No systemd unit changes are needed unless flags or paths changed.
