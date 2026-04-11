# KVM-Center

A lightweight, centralized management platform for KVM hypervisor fleets. Inspired by VMware vCenter, built for homelabs.

## 🎯 Project Overview

KVM-Center provides a **single pane of glass** web UI to manage multiple KVM hypervisors from one central location. It consists of:

- **Control Plane Server**: FastAPI-based web server with SQLite backend
- **Node Agents**: Lightweight Python agents deployed on each hypervisor
- **Web Dashboard**: Real-time VM and host monitoring with power controls

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    KVM-Center Server                         │
│                  (FastAPI + SQLite)                          │
│                  Port: 8090 (HTTP)                           │
│                                                              │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐  │
│  │  Web Dashboard │  │  REST API   │  │  Command Router   │  │
│  │  (HTML/JS)   │  │  /api/*     │  │  (to agents)       │  │
│  └─────────────┘  └─────────────┘  └─────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
                              │
         ┌────────────────────┼────────────────────┐
         │                    │                    │
         ▼                    ▼                    ▼
┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
│   hvnode01      │ │   hvnode02      │ │   hvnode03      │
│  (REDACTED-HOST-11) │ │  (REDACTED-HOST-12) │ │  (REDACTED-HOST-13) │
│                 │ │                 │ │                 │
│  ┌───────────┐  │ │  ┌───────────┐  │ │  ┌───────────┐  │
│  │  Agent    │  │ │  │  Agent    │  │ │  │  Agent    │  │
│  │ Port 8081 │  │ │  │ Port 8081 │  │ │  │ Port 8081 │  │
│  └───────────┘  │ │  └───────────┘  │ │  └───────────┘  │
│                 │ │                 │ │                 │
│  ┌───────────┐  │ │  ┌───────────┐  │ │  ┌───────────┐  │
│  │  libvirt  │  │ │  │  libvirt  │  │ │  │  libvirt  │  │
│  │  (KVM)    │  │ │  │  (KVM)    │  │ │  │  (KVM)    │  │
│  └───────────┘  │ │  └───────────┘  │ │  └───────────┘  │
└─────────────────┘ └─────────────────┘ └─────────────────┘
```

## 🚀 Features

### Current (v1.0)
- ✅ Real-time host monitoring (CPU, RAM, uptime)
- ✅ VM inventory across all hosts
- ✅ VM power state tracking (RUNNING, SHUTOFF, PAUSED)
- ✅ Remote power control (Start, Stop, Restart)
- ✅ Searchable VM list
- ✅ Responsive web UI (mobile-friendly)
- ✅ Automatic telemetry collection (10-second intervals)

### Planned (v2.0)
- [ ] VM console access (VNC/SPICE integration)
- [ ] Snapshot management
- [ ] VM migration between hosts
- [ ] Storage pool monitoring
- [ ] Network interface management
- [ ] Alerting and notifications
- [ ] User authentication & RBAC
- [ ] VM creation wizard
- [ ] Resource scheduling & DRS-like features

## 📁 Project Structure

```
kvm-center/
├── server.py              # FastAPI control plane server
├── agent.py               # Node agent (deployed to hypervisors)
├── docker-compose.yml     # Docker deployment configuration
├── Dockerfile             # Server container build
├── requirements.txt       # Python dependencies
├── onboard_agent.sh       # Agent deployment script
└── docs/
    └── DEPLOYMENT.md      # Detailed deployment guide
```

## 🛠️ Deployment

### Prerequisites

**Control Plane Server:**
- Docker & Docker Compose
- Network access to all hypervisors

**Hypervisor Nodes:**
- Rocky Linux 9+ (or Ubuntu/Debian)
- KVM/libvirt installed and running
- Python 3.9+
- SSH access from control plane

### Quick Start

#### 1. Deploy Control Plane Server

```bash
cd /opt/kvm-center
docker compose up -d
```

Server will be available at: `http://<server-ip>:8090`

#### 2. Deploy Agents to Hypervisors

```bash
# From the control plane host
./onboard_agent.sh hvnode01
./onboard_agent.sh hvnode02
./onboard_agent.sh hvnode03
```

Or manually on each hypervisor:

```bash
# Install dependencies
dnf install -y python3-libvirt python3-pip python3-devel gcc

# Deploy agent
mkdir -p /opt/kvm-center-agent
# Copy agent.py to /opt/kvm-center-agent/

# Install Python deps
pip3 install requests psutil flask

# Create systemd service (see onboard_agent.sh for template)
systemctl enable --now kvm-center-agent
```

### Configuration

**Server Environment Variables:**
- `PYTHONUNBUFFERED=1` (default)

**Agent Configuration:**
Edit `agent.py` to change:
- `SERVER_URL`: Control plane endpoint
- `COMMAND_PORT`: Agent command listener port (default: 8081)
- `CHECK_INTERVAL`: Telemetry frequency (default: 10 seconds)

## 🔌 API Reference

### Telemetry Ingestion

```http
POST /api/telemetry
Content-Type: application/json

{
  "host_id": "hvnode01.example.lan",
  "cpu_usage": 45.2,
  "mem_usage": 62.1,
  "vms": [
    {
      "name": "Web-Server",
      "id": "7e0141bcd78448f6b0904cb34cd9c85d",
      "state": "RUNNING",
      "cpu": 4,
      "mem": 8388608
    }
  ],
  "timestamp": "2026-04-11T01:30:00.000000"
}
```

### List Hosts

```http
GET /api/hosts
```

### List VMs

```http
GET /api/vms
```

### Execute VM Command

```http
POST /api/command
Content-Type: application/json

{
  "vm_id": "7e0141bcd78448f6b0904cb34cd9c85d",
  "action": "STOP"
}
```

**Supported Actions:**
- `START` - Power on VM
- `STOP` - Hard power off (immediate)
- `RESTART` - Stop then start
- `SHUTDOWN` - Graceful ACPI shutdown

## 🔧 Troubleshooting

### Agents Not Reporting

1. Check agent status:
   ```bash
   systemctl status kvm-center-agent
   journalctl -u kvm-center-agent -f
   ```

2. Verify connectivity:
   ```bash
   curl http://<server-ip>:8090/api/hosts
   ```

3. Check libvirt connection:
   ```bash
   virsh list --all
   ```

### Commands Not Executing

1. Verify agent command port is open:
   ```bash
   netstat -tlnp | grep 8081
   ```

2. Test command endpoint from server:
   ```bash
   curl -X POST http://<agent-ip>:8081/command \
     -H "Content-Type: application/json" \
     -d '{"vm_id": "<uuid>", "action": "STOP"}'
   ```

3. Check firewall rules on hypervisors

### Database Issues

Reset database (will repopulate from agents):
```bash
docker exec kvm-center-server rm /data/kvm_center.db
docker compose restart
```

## 📝 Development

### Running Locally

```bash
# Server
pip install -r requirements.txt
python server.py

# Agent (on hypervisor or test VM)
pip install requests psutil flask python-libvirt
python agent.py
```

### Building Docker Image

```bash
docker build -t kvm-center-server .
```

## 📄 License

MIT License - See LICENSE file for details.

## 🙏 Acknowledgments

- Inspired by VMware vCenter's centralized management approach
- Built with FastAPI, Flask, and libvirt
- Designed for homelab and small-scale deployments

---

**Version:** 1.0.0  
**Last Updated:** 2026-04-11  
**Author:** Donovan Muskett-Yetts
