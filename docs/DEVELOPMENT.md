# Development Guide

This guide covers building from source, running locally, adding new features, and understanding the code layout.

---

## Prerequisites

- **Go 1.22+** — `go version`
- **Docker** (optional, for container builds)
- **virsh** (optional, for testing the agent locally on a KVM host)

---

## Repository Layout

```
kvm-center/
├── go.mod                    # Module: kvm-center, Go 1.22, no external deps
│
├── internal/
│   └── models/
│       └── models.go         # Shared types used by both binaries
│
├── server/
│   ├── main.go               # HTTP server, routes, background jobs, embed directive
│   ├── store.go              # In-memory store with JSON file persistence
│   ├── handlers.go           # HTTP handler functions
│   └── static/
│       └── index.html        # Web UI — embedded into the server binary at build time
│
├── agent/
│   ├── main.go               # CLI flags, telemetry loop, signal handling
│   ├── collector.go          # /proc + virsh metrics collection
│   └── commander.go          # HTTP server for receiving commands
│
├── webui/
│   ├── Dockerfile            # nginx:alpine container for the UI
│   ├── index.html            # Copy of server/static/index.html for Docker builds
│   ├── nginx.conf.template   # envsubst template — proxies /api/ to the server
│   └── docker-entrypoint.sh  # Runs envsubst then starts nginx
│
├── scripts/
│   ├── bootstrap.sh          # Operational launcher (--stack, --deploy, etc.)
│   └── install-agent.sh      # One-shot agent installer for hypervisors
│
├── Dockerfile.server         # Two-stage build: golang:1.22-alpine → alpine
├── Dockerfile.build          # Cross-compile both binaries + download portal
├── docker-compose.yml        # Full stack (server + webui)
├── docker-compose.server.yml # Server only
└── Makefile                  # Build targets
```

---

## Building Locally

```bash
# Build both binaries
make build

# Build server only
make build-server        # → ./kvm-center-server

# Build agent only
make build-agent         # → ./kvm-center-agent

# Cross-compile for Linux (amd64 + arm64)
make dist                # → dist/

# Run linter
make vet

# Format code
make fmt
```

---

## Running Locally

### Server

```bash
make run-server
# or:
KVM_CENTER_DATA=./data.json go run ./server -addr :8090
```

Open `http://localhost:8090` — the embedded web UI is served from the Go binary.

The server creates `./data.json` on first run. Delete it to reset state.

### Agent (on a KVM host or VM with libvirt)

```bash
make run-agent
# or:
go run ./agent -server http://localhost:8090

# Custom interval for faster testing:
go run ./agent -server http://localhost:8090 -interval 5s
```

### Quick smoke test without a real hypervisor

You can POST fake telemetry directly to test the server:

```bash
curl -X POST http://localhost:8090/api/telemetry \
  -H "Content-Type: application/json" \
  -d '{
    "host_id": "test-node",
    "ip": "192.168.1.100",
    "agent_port": 8081,
    "cpu_usage": 42.5,
    "cpu_count": 8,
    "mem_usage": 68.2,
    "mem_total_mb": 32768,
    "mem_free_mb": 10000,
    "uptime": "3h 12m",
    "os": "Rocky Linux 9.7",
    "kernel_ver": "5.14.0",
    "libvirt_ver": "10.0.0",
    "timestamp": "2026-04-11T12:00:00Z",
    "vms": [
      {
        "uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
        "name": "test-vm",
        "state": "running",
        "cpu_count": 2,
        "cpu_usage": 15.0,
        "mem_total_mb": 4096,
        "mem_usage_mb": 2048,
        "disk_path": "/var/lib/libvirt/images/test.qcow2",
        "disk_usage_gb": 10.5,
        "disk_total_gb": 50.0,
        "networks": []
      }
    ],
    "storage": [
      {
        "name": "default",
        "path": "/var/lib/libvirt/images",
        "capacity_gb": 500.0,
        "allocation_gb": 100.0,
        "available_gb": 400.0,
        "state": "running",
        "pool_type": "dir"
      }
    ]
  }'
```

---

## Code Walkthrough

### Shared Models (`internal/models/models.go`)

Both binaries import this package. It contains all data types. Adding a new field to a metric requires changes here first, then in the collector and the API handlers.

Key types:
- `TelemetryPayload` — everything an agent sends in one request
- `Host`, `VM`, `StoragePool` — what the server stores and returns via API
- `APICommandRequest` — from browser to server
- `CommandRequest` — from server to agent
- `CommandResponse` — agent result

### Server Store (`server/store.go`)

The store is a simple struct with three maps protected by a single `sync.RWMutex`:

```go
type Store struct {
    mu       sync.RWMutex
    hosts    map[string]*models.Host
    vms      map[string][]*models.VM          // host_id → []VM
    storage  map[string][]*models.StoragePool // host_id → []Pool
    dataFile string
}
```

`ApplyTelemetry` is the only write path. It upserts the host and **atomically replaces** the VM and storage slices for that host. This means stale VMs are cleaned up automatically — if a VM is deleted on the hypervisor, it simply won't appear in the next telemetry payload.

The store saves to disk every 30 seconds via a background goroutine in `main.go`. Reading happens on every API call with a read lock.

### Server Handlers (`server/handlers.go`)

Handlers follow the pattern `handleX(store *Store) http.HandlerFunc`. Each returns a closure over the store. Error responses always use `writeError(w, status, msg)` which writes `{"error": "..."}`.

Adding a new endpoint:
1. Write a handler function in `handlers.go`
2. Register it in `main.go`: `mux.HandleFunc("/api/new-path", handleNew(store))`

### Agent Collector (`agent/collector.go`)

The collector runs on every telemetry tick. It makes several `virsh` subprocess calls in sequence. The calling pattern is always:

```go
out, err := exec.Command("virsh", "subcommand", "arg").Output()
if err != nil {
    // log and return zero value — never crash on a single metric failure
}
// parse out
```

**CPU delta tracking** uses two package-level maps (`prevCPU` and `prevVMCPU`) that persist between ticks. These are not persisted to disk — they reset on agent restart, which means the first report after a restart shows 0% CPU.

**Adding a new metric:**
1. Add the field to the relevant struct in `internal/models/models.go`
2. Collect it in `collector.go` (usually a new `virsh` call or `/proc` read)
3. Set it in `buildVM()` or `CollectTelemetry()`
4. Return it in the existing JSON serialisation (no changes needed — `json` tags handle it)
5. Display it in `server/static/index.html`

### Agent Commander (`agent/commander.go`)

A minimal `net/http` server listening on the configured port. The `handleCommand` function:
1. Decodes the `CommandRequest` JSON
2. Switches on `action` string
3. Runs the appropriate `virsh` command via `exec.Command`
4. Returns `CommandResponse{Success: true/false, Message: virsh_output}`

Adding a new action: add a new `case` to the switch statement.

### Web UI (`server/static/index.html`)

A single HTML file with embedded CSS and vanilla JS. No build step, no bundler, no framework. The entire UI state is in a single `state` object. `render()` is called after every data fetch and navigation event.

The UI calls these API endpoints:
- `GET /api/hosts` — every 15 seconds
- `GET /api/vms` — every 15 seconds
- `GET /api/storage` — every 15 seconds
- `POST /api/command` — on user action

**The `webui/index.html` file is a copy of `server/static/index.html`.** When you edit the UI, update **both files** (or just `server/static/index.html` and run `cp server/static/index.html webui/index.html` before committing). The server binary embeds `server/static/index.html` at build time; the nginx container uses `webui/index.html`.

---

## Docker Builds

```bash
# Full stack
make docker-build    # builds both images
make docker-up       # starts containers
make docker-down     # stops and removes containers
make docker-logs     # tail all container logs

# Build-and-serve container (agent downloads portal)
make docker-build-server
# Access at http://localhost:8080 for agent binary downloads
```

The two-stage `Dockerfile.server` build:
1. Stage 1 (`golang:1.22-alpine`): compiles `./server` to `/out/kvm-center-server`
2. Stage 2 (`alpine:latest`): copies only the binary and CA certificates

The final image is ~15 MB. `CGO_ENABLED=0` ensures a fully static binary.

---

## Bootstrap Script Reference

```bash
./scripts/bootstrap.sh --server              # Run server locally (go run)
./scripts/bootstrap.sh --agent               # Run agent locally (go run)
./scripts/bootstrap.sh --stack               # docker compose build + up
./scripts/bootstrap.sh --build               # Run Dockerfile.build on port 8080
./scripts/bootstrap.sh --deploy              # git pull + docker compose rebuild
./scripts/bootstrap.sh --logs                # docker compose logs -f
./scripts/bootstrap.sh --status              # docker compose ps
./scripts/bootstrap.sh --stop                # docker compose down
./scripts/bootstrap.sh --deploy-agent        # Cross-compile agent + print scp/ssh commands
```

---

## Adding a New Feature

### Example: Add disk read/write IOPS to VM metrics

1. **Model** — add fields to `VM` in `internal/models/models.go`:
   ```go
   DiskReadIOPS  float64 `json:"disk_read_iops"`
   DiskWriteIOPS float64 `json:"disk_write_iops"`
   ```

2. **Collector** — in `agent/collector.go`, inside `buildVM()`:
   ```go
   // After getting disk path from XML
   vm.DiskReadIOPS, vm.DiskWriteIOPS = diskIOPS(name, targetDev)
   ```
   Add the `diskIOPS` function:
   ```go
   func diskIOPS(vmName, dev string) (readIops, writeIops float64) {
       out, err := exec.Command("virsh", "domblkstat", vmName, dev).Output()
       // parse rd_req and wr_req fields, compute delta...
   }
   ```

3. **UI** — in `server/static/index.html`, add to the VM table or detail view:
   ```html
   <td>${v.disk_read_iops.toFixed(0)} r/s</td>
   ```

4. **Sync webui copy:**
   ```bash
   cp server/static/index.html webui/index.html
   ```

5. **Rebuild and redeploy:**
   ```bash
   git add -A && git commit -m "feat: add disk IOPS to VM metrics"
   git push gitea main
   ssh root@REDACTED-HOST "cd /opt/kvm-center && git pull && docker compose build && docker compose up -d"
   ```

---

## Redeploy Workflow

```bash
# 1. Make changes locally
# 2. Commit
git add -A && git commit -m "your message"

# 3. Push to Gitea
git push gitea main

# 4. Pull and rebuild on docker host
ssh root@REDACTED-HOST "cd /opt/kvm-center && git pull && docker compose build && docker compose up -d"

# 5. If agent changed, rebuild and redeploy agents
ssh root@REDACTED-HOST "cd /opt/kvm-center && \
  docker build -f Dockerfile.build -t kvm-center-build:latest . && \
  docker create --name kvm-builder kvm-center-build:latest && \
  docker cp kvm-builder:/dist/kvm-center-agent-amd64 ./kvm-center-agent-amd64 && \
  docker rm kvm-builder && \
  for NODE in REDACTED-HOST-11 REDACTED-HOST-12 REDACTED-HOST-13; do
    scp kvm-center-agent-amd64 root@\$NODE:/tmp/
    ssh root@\$NODE 'cp /tmp/kvm-center-agent-amd64 /opt/kvm-center/kvm-center-agent && systemctl restart kvm-center-agent'
  done"
```

---

## CI security gates (v5.20.5+)

`.gitea/workflows/test.yml` runs three scanners on every push and PR
alongside the gofmt / vet / test suite. All three have escape hatches
so a blocker can be unstuck quickly.

| Gate | Scope | Failure mode | Triage |
|---|---|---|---|
| **govulncheck** | Go stdlib + direct + transitive deps, **reachable symbols only** | Blocking on any finding | Bump the affected module. When no fix exists, pin to a tolerated tag and track the CVE in the PR. |
| **npm audit --audit-level=critical** | webui-react production deps | Blocking on critical | `npm audit fix` locally; if a major bump is required, file it as a follow-up and note the CVE. |
| **npm audit --audit-level=high** | same, warn-only | Non-blocking, but the summary appears in the CI log / email | Track for the next release. |
| **gitleaks** | full working tree + git history | Blocking on any match | **Rotate the leaked credential first**, then rewrite or `git filter-repo` the commit. Add a `gitleaks:allow` fingerprint only for verified false positives. |

The `summary` job aggregates all three so Gitea's failure email names
which gate tripped.

---

## Planned Improvements

| Feature | Notes |
|---|---|
| VM console access | noVNC or SPICE web proxy through the server |
| Historical metrics | Time-series CPU/RAM graphs using the existing InfluxDB on docker host |
| Telegram alerts | Host offline / VM crash notifications via bot token in SERVICES.md |
| Authentication | Simple token-based auth gate in front of `/api/command` |
| VM creation | New POST `/api/vm` endpoint; virsh define + XML template |
| CI/CD pipeline | Gitea Actions workflow mirroring host-monitor's deploy.yml pattern |
| Snapshot list per VM | `virsh snapshot-list <vm>` + new GET `/api/vms/{id}/snapshots` |
