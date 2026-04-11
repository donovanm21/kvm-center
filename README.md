# KVM Center

A lightweight, centralised management platform for KVM hypervisor fleets. Designed for homelabs — a single pane of glass for your VMs, inspired by VMware vCenter.

Built in Go. Zero external dependencies. Single static binaries.

**Live at:** `http://docker.example.lan:8096`

---

## What It Does

- **Dashboard** — host grid with real-time CPU and memory bars
- **VM inventory** — all VMs across all hypervisors in one table, searchable
- **Power control** — Start, Stop, Restart, Snapshot from the UI
- **Storage pools** — capacity and utilisation per hypervisor
- **Offline detection** — hosts marked offline if no telemetry for 2 minutes
- **Dark theme** — vCenter-inspired layout that isn't painful to look at

---

## Architecture at a Glance

```
Browser → :8096 → nginx (webui container)
                     └─ /api/* → :8090 → kvm-center-server (Go)
                                              ↑ POST /api/telemetry (every 30s)
                                              ↓ POST /command (on user action)
                                   hvnode01:8081  hvnode02:8081  hvnode03:8081
                                   (kvm-center-agent systemd service)
```

Two binaries. One Docker Compose stack. Three systemd services.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full design.

---

## Quick Start

### Server Stack (Docker)

```bash
# Clone
git clone http://jeff:Access2Git@REDACTED-HOST:9001/Homelab/kvm-center.git /opt/kvm-center
cd /opt/kvm-center

# Build and start
docker compose build
docker compose up -d

# Verify
curl http://localhost:8090/health
```

**UI:** `http://REDACTED-HOST:8096`  
**API:** `http://REDACTED-HOST:8090`

### Agents (on each hypervisor)

```bash
# Build agent binary on the docker host
docker build -f Dockerfile.build -t kvm-center-build:latest .
docker create --name kvm-builder kvm-center-build:latest
docker cp kvm-builder:/dist/kvm-center-agent-amd64 ./kvm-center-agent-amd64
docker rm kvm-builder

# Deploy to all hypervisors
for NODE in REDACTED-HOST-11 REDACTED-HOST-12 REDACTED-HOST-13; do
  scp kvm-center-agent-amd64 scripts/install-agent.sh root@$NODE:/tmp/
  ssh root@$NODE "chmod +x /tmp/kvm-center-agent-amd64 /tmp/install-agent.sh && \
    /tmp/install-agent.sh --server http://REDACTED-HOST:8090"
done
```

Agents will start reporting within 30 seconds. Refresh the UI to see your hosts and VMs.

---

## Deployment Summary

| Component | Host | Port | Container / Service |
|---|---|---|---|
| API server | docker.example.lan | 8090 | `kvm-center-server` |
| Web UI (nginx) | docker.example.lan | 8096 | `kvm-center-webui` |
| Agent | hvnode01 | 8081 | `kvm-center-agent.service` |
| Agent | hvnode02 | 8081 | `kvm-center-agent.service` |
| Agent | hvnode03 | 8081 | `kvm-center-agent.service` |

Data persists in Docker volume `kvm-center_kvm-center-data` as a JSON file.  
Code lives in Gitea at `http://REDACTED-HOST:9001/Homelab/kvm-center`.

---

## Useful Commands

```bash
# Server logs
docker logs kvm-center-server -f

# Restart stack
docker compose restart

# Check what the API sees
curl -s http://REDACTED-HOST:8090/api/hosts | python3 -m json.tool
curl -s http://REDACTED-HOST:8090/api/vms   | python3 -m json.tool

# Agent logs on a hypervisor
ssh root@REDACTED-HOST-11 "journalctl -u kvm-center-agent -f"

# Rebuild after code change
cd /opt/kvm-center && git pull && docker compose build && docker compose up -d

# Bootstrap script modes
./scripts/bootstrap.sh --status        # docker compose ps
./scripts/bootstrap.sh --logs          # tail logs
./scripts/bootstrap.sh --deploy        # git pull + rebuild
./scripts/bootstrap.sh --stop          # docker compose down
./scripts/bootstrap.sh --deploy-agent  # cross-compile agent + print deploy commands
```

---

## Documentation

| Doc | Contents |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Design decisions, data flow diagrams, component overview, persistence model |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | Full deployment guide, firewall config, updating, backup/restore, troubleshooting |
| [docs/AGENT.md](docs/AGENT.md) | Metrics collected, virsh commands, CPU calculation, systemd reference |
| [docs/API.md](docs/API.md) | Complete REST API reference with request/response examples |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | Building from source, running locally, code walkthrough, adding features |

---

## Project Structure

```
kvm-center/
├── internal/models/     Shared Go types (Host, VM, StoragePool, payloads)
├── server/              Control plane binary + embedded web UI
│   ├── main.go          HTTP server, routes, background goroutines
│   ├── store.go         In-memory store, JSON persistence
│   ├── handlers.go      API handlers
│   └── static/index.html  Single-page vCenter-style dashboard
├── agent/               Hypervisor agent binary
│   ├── main.go          Telemetry loop, signal handling
│   ├── collector.go     /proc + virsh metric collection
│   └── commander.go     Command HTTP server (virsh execution)
├── webui/               nginx container for the UI
├── scripts/             bootstrap.sh, install-agent.sh
├── Dockerfile.server    Two-stage Go build → ~15 MB alpine image
├── Dockerfile.build     Cross-compile both binaries + download portal
├── docker-compose.yml   Full stack
└── Makefile             Build targets
```

---

**Gitea:** `http://REDACTED-HOST:9001/Homelab/kvm-center`  
**Last deployed:** 2026-04-11
