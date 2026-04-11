# Architecture

KVM Center is a lightweight, centralized management platform for KVM hypervisor fleets. It follows a client-server model — a stateful control plane runs on a dedicated management host (Docker), and a thin agent runs as a systemd service on each hypervisor.

---

## High-Level Design

```
                           ┌─────────────────────────────────────────────┐
                           │            docker.example.lan               │
                           │                                             │
  Browser :8096  ──────►  │  ┌──────────────────┐                       │
                           │  │  kvm-center-webui│  nginx:alpine         │
                           │  │  (port 80)       │  proxies /api/ →      │
                           │  └────────┬─────────┘  :8090                │
                           │           │                                  │
                           │  ┌────────▼─────────┐                       │
                           │  │ kvm-center-server│  Go binary             │
                           │  │  (port 8090)     │  net/http              │
                           │  │                  │  in-memory store       │
                           │  │  /api/telemetry  │  JSON persistence      │
                           │  │  /api/hosts      │                        │
                           │  │  /api/vms        │                        │
                           │  │  /api/storage    │                        │
                           │  │  /api/command    │                        │
                           │  └──────────────────┘                       │
                           └──────────┬──────────────────────────────────┘
                                      │  POST /command (per-agent)
                   ┌──────────────────┼──────────────────┐
                   │                  │                  │
                   ▼                  ▼                  ▼
        ┌──────────────────┐ ┌──────────────────┐ ┌──────────────────┐
        │    hvnode01      │ │    hvnode02      │ │    hvnode03      │
        │  REDACTED-HOST    │ │  REDACTED-HOST    │ │  REDACTED-HOST    │
        │                  │ │                  │ │                  │
        │  kvm-center-     │ │  kvm-center-     │ │  kvm-center-     │
        │  agent (systemd) │ │  agent (systemd) │ │  agent (systemd) │
        │                  │ │                  │ │                  │
        │  telemetry ──►   │ │  telemetry ──►   │ │  telemetry ──►   │
        │  cmd server :8081│ │  cmd server :8081│ │  cmd server :8081│
        │                  │ │                  │ │                  │
        │  libvirt / virsh │ │  libvirt / virsh │ │  libvirt / virsh │
        └──────────────────┘ └──────────────────┘ └──────────────────┘
```

---

## Components

### kvm-center-server

A single Go binary compiled with `CGO_ENABLED=0` (no C dependencies). It runs as a Docker container and is responsible for:

- **Receiving telemetry** from agents (`POST /api/telemetry`)
- **Storing state** in memory and persisting it to a JSON file
- **Routing commands** to the correct agent when a user takes an action
- **Serving the web UI** as embedded static files (via `//go:embed`)
- **Detecting offline hosts** via a background goroutine (2-minute timeout)

The server has no database process to manage. State lives in a `sync.RWMutex`-protected in-memory struct and is written to `/var/lib/kvm-center/data.json` every 30 seconds. On restart, state is reloaded from that file.

**Source:** `server/main.go`, `server/store.go`, `server/handlers.go`

### kvm-center-agent

A single Go binary deployed via systemd on each hypervisor. It has two concurrent responsibilities:

- **Telemetry loop** — collects host and VM metrics every 30 seconds and POSTs them to the server
- **Command server** — listens on port 8081 for `POST /command` requests from the server and executes the corresponding `virsh` command

The agent uses **no external Go packages**. Metrics collection is done by reading `/proc/stat`, `/proc/meminfo`, `/proc/uptime`, and `/etc/os-release` directly, and by shelling out to `virsh` for libvirt data. VM CPU usage is computed as a delta between two `virsh domstats` readings.

**Source:** `agent/main.go`, `agent/collector.go`, `agent/commander.go`

### kvm-center-webui

An nginx:alpine container that serves a single-page HTML application and reverse-proxies all `/api/` requests to the Go server. Using a separate nginx container:

- Avoids CORS issues between the UI and API
- Keeps the Go server stateless and cacheable
- Lets nginx handle connection management and static asset caching
- Allows the UI port (8096) to be decoupled from the API port (8090)

The HTML/JS application is a single file with no framework dependencies. It polls the API every 15 seconds and renders the current state.

**Source:** `webui/index.html`, `webui/nginx.conf.template`, `webui/Dockerfile`

---

## Data Flow

### Telemetry (Agent → Server)

```
1. Agent wakes on 30-second interval
2. Reads /proc/stat  → compute host CPU% (delta since last reading)
3. Reads /proc/meminfo → total and available RAM
4. Runs `virsh list --all` → VM names and states
5. For each VM:
   a. `virsh dominfo <name>`      → UUID, CPU count, max/used RAM
   b. `virsh dumpxml <name>`      → disk path, network interfaces (parsed as XML)
   c. `virsh domstats --cpu-total`→ CPU nanoseconds (delta → CPU%)
   d. `virsh domifstat <iface>`   → RX/TX bytes per NIC
   e. syscall.Statfs on disk path → disk GB used/total
6. Runs `virsh pool-list --all` → storage pools
7. For each pool:
   a. `virsh pool-info <name>`    → capacity, allocation, available
   b. `virsh pool-dumpxml <name>` → pool path
8. Builds TelemetryPayload JSON and POSTs to http://<server>:8090/api/telemetry
```

### Command (Browser → Server → Agent)

```
1. User clicks Start/Stop/Restart/Snapshot in the UI
2. UI POSTs to /api/command:
   { host_id, vm_name, vm_uuid, action, params }
3. Server looks up agent address from store: ip:agent_port
4. Server forwards to http://<agent_ip>:8081/command:
   { action, vm_name, vm_uuid, params }
5. Agent executes virsh command:
   START    → virsh start <vm_name>
   STOP     → virsh shutdown <vm_name>  (graceful ACPI)
   RESTART  → virsh reboot <vm_name>
   SNAPSHOT → virsh snapshot-create-as <vm_name> <snap_name>
   MIGRATE  → virsh migrate --live --persistent <vm_name> qemu+ssh://<dest>/system
6. Agent returns { success, message }
7. Server relays response to browser
8. UI shows toast notification
```

### Offline Detection

A background goroutine on the server ticks every 30 seconds. Any host whose `last_seen` timestamp is more than 2 minutes in the past is marked `offline`. The store is also written to disk on this same tick.

---

## Shared Types (`internal/models`)

Both binaries share a common Go package at `internal/models/models.go`. This ensures the telemetry payload the agent serialises is exactly what the server deserialises, without any duplication.

Key types:

| Type | Direction | Purpose |
|---|---|---|
| `TelemetryPayload` | Agent → Server | Full host + VM + storage snapshot |
| `Host` | Server store | Hypervisor state |
| `VM` | Server store | Virtual machine state |
| `StoragePool` | Server store | libvirt storage pool |
| `APICommandRequest` | Browser → Server | User-initiated action |
| `CommandRequest` | Server → Agent | Forwarded command |
| `CommandResponse` | Agent → Server | Result of virsh execution |

---

## Persistence

The server uses a flat JSON file rather than a database. This is an intentional choice:

- **Zero external dependencies** — no SQLite CGO, no Postgres process
- **Human-readable** — the data file can be inspected and edited directly
- **Adequate at scale** — the homelab fleet (3 hosts, ~13 VMs) produces a data file of a few KB
- **Survives restarts** — on startup the store is loaded from disk; agents re-report within 30 seconds to refresh any stale data

The data file location defaults to `/var/lib/kvm-center/data.json` and is configurable via `KVM_CENTER_DATA`.

---

## Network Ports

| Port | Host | Direction | Purpose |
|---|---|---|---|
| **8090** | docker.example.lan | inbound | Server API (telemetry ingestion, command routing, embedded UI) |
| **8096** | docker.example.lan | inbound | nginx web UI (proxies /api/ to :8090) |
| **8081** | hvnode01/02/03 | inbound from server | Agent command server |

---

## Build & Image Strategy

Following the same pattern as `host-monitor`:

- **`Dockerfile.server`** — two-stage build. Stage 1: `golang:1.22-alpine` compiles the server binary. Stage 2: `alpine:latest` runtime with only the binary and CA certificates. Final image is ~15 MB.
- **`Dockerfile.build`** — single stage that cross-compiles both `kvm-center-server` and `kvm-center-agent` for `linux/amd64` and `linux/arm64`, packages them with `install-agent.sh` into a `/dist` directory, then serves that directory via `python3 -m http.server`. Used for agent distribution.
- **`webui/Dockerfile`** — `nginx:alpine` with `envsubst` used at runtime to substitute `KVM_CENTER_SERVER_HOST` and `KVM_CENTER_SERVER_PORT` into the nginx config template.

---

## Design Decisions

### Why Go instead of Python?

| Concern | Python (v1) | Go (v2) |
|---|---|---|
| Distribution | pip install, venv, requirements.txt | Single static binary |
| Agent install | 6-step manual process | Copy binary + run install script |
| Dependencies | FastAPI, Flask, libvirt-python, psutil, requests | Zero (stdlib only) |
| Agent libvirt access | libvirt Python bindings (CGO) | `virsh` via `os/exec` (no CGO) |
| Memory | ~75 MB per process | ~8 MB per process |
| Cold start | ~2 seconds | <100 ms |
| Cross-compile | No | Yes (`GOOS=linux GOARCH=amd64`) |

### Why `virsh` instead of libvirt Go bindings?

The two main Go libvirt options are `github.com/digitalocean/go-libvirt` (pure Go RPC client) and `libvirt-go` (CGO bindings). Both introduce external dependencies and complicate cross-compilation.

`virsh` is always present on any KVM host where libvirt is running. Shelling out to it keeps the agent a single static binary that can be copied and run anywhere without installing Go or any libraries.

The trade-off is subprocess overhead (~20ms per virsh call). For a 30-second telemetry interval this is acceptable.

### Why in-memory store instead of SQLite?

SQLite with CGO works fine but breaks `CGO_ENABLED=0` cross-compilation. `modernc.org/sqlite` (pure Go) adds ~8 MB to the binary. For 3 hosts and 13 VMs, a JSON file is perfectly adequate and keeps the server dependency-free.

### Why nginx instead of serving the UI from Go?

The Go server does embed and serve the HTML for standalone deployments (`go:embed static`). The nginx container exists because:

1. The browser makes API calls to the same origin, so no CORS headers are needed
2. nginx handles connection pooling and keep-alive more efficiently than Go's `net/http` for static assets
3. Port separation (8096 for UI, 8090 for API) keeps the deployment flexible
