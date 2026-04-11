# Deployment Guide

This guide covers deploying the KVM Center server stack on Docker and installing agents on KVM hypervisors. For architecture context see [ARCHITECTURE.md](ARCHITECTURE.md).

---

## Prerequisites

### Control Plane Host (docker.example.lan)
- Docker Engine 24+
- Docker Compose v2
- Port 8090 and 8096 available
- Network access to each hypervisor on port 8081

### Hypervisor Nodes (hvnode01–03)
- Rocky Linux 9+ (or any systemd-based Linux)
- `libvirtd` running with `virsh` available
- Port 8081 available (inbound from control plane)
- Outbound access to control plane on port 8090

---

## Server Stack Deployment

### Step 1 — Get the Code on the Docker Host

The code lives in Gitea at `http://REDACTED-HOST:9001/Homelab/kvm-center`.

**First-time clone:**
```bash
git clone http://jeff:Access2Git@REDACTED-HOST:9001/Homelab/kvm-center.git /opt/kvm-center
cd /opt/kvm-center
```

**Update existing install:**
```bash
cd /opt/kvm-center && git pull
```

### Step 2 — Build and Start

```bash
cd /opt/kvm-center
docker compose build
docker compose up -d
```

Or use the bootstrap script:
```bash
./scripts/bootstrap.sh --stack
```

### Step 3 — Verify

```bash
# Container health
docker compose ps

# API health check
curl http://localhost:8090/health

# Should return empty array before agents connect
curl http://localhost:8090/api/hosts
```

**Web UI:** `http://REDACTED-HOST:8096`  
**API:** `http://REDACTED-HOST:8090`

---

## Agent Deployment

### Automated (Recommended)

The build container cross-compiles the agent binary and the `install-agent.sh` script handles everything else.

**From the docker host — build agent binaries:**
```bash
cd /opt/kvm-center

# Build the cross-compile container and extract binaries
docker build -f Dockerfile.build -t kvm-center-build:latest .
docker create --name kvm-builder kvm-center-build:latest
docker cp kvm-builder:/dist/kvm-center-agent-amd64 ./kvm-center-agent-amd64
docker rm kvm-builder
```

**Copy and install on each hypervisor:**
```bash
for NODE in REDACTED-HOST-11 REDACTED-HOST-12 REDACTED-HOST-13; do
  scp kvm-center-agent-amd64 scripts/install-agent.sh root@$NODE:/tmp/
  ssh root@$NODE "chmod +x /tmp/kvm-center-agent-amd64 /tmp/install-agent.sh && \
    /tmp/install-agent.sh --server http://REDACTED-HOST:8090"
done
```

Or use the bootstrap script to do it all:
```bash
./scripts/bootstrap.sh --deploy-agent
```
Then follow the printed scp/ssh instructions for each node.

### Manual Install

On each hypervisor as root:

```bash
# 1. Create install directory
mkdir -p /opt/kvm-center

# 2. Copy binary (from wherever it was built)
cp kvm-center-agent-amd64 /opt/kvm-center/kvm-center-agent
chmod +x /opt/kvm-center/kvm-center-agent

# 3. Write systemd unit
cat > /etc/systemd/system/kvm-center-agent.service << 'EOF'
[Unit]
Description=KVM Center Agent
After=network.target libvirtd.service
Wants=libvirtd.service

[Service]
Type=simple
ExecStart=/opt/kvm-center/kvm-center-agent \
  -server http://REDACTED-HOST:8090 \
  -port 8081 \
  -interval 30s
Restart=always
RestartSec=10
StandardOutput=append:/var/log/kvm-center-agent.log
StandardError=append:/var/log/kvm-center-agent.log
SupplementaryGroups=libvirt kvm

[Install]
WantedBy=multi-user.target
EOF

# 4. Enable and start
systemctl daemon-reload
systemctl enable kvm-center-agent
systemctl start kvm-center-agent

# 5. Check status
systemctl status kvm-center-agent
```

### install-agent.sh Reference

The installer script accepts these flags:

```
--server <url>       Control plane URL (required)
                     e.g. http://REDACTED-HOST:8090

--port <port>        Agent command port (default: 8081)
                     Must be reachable from the server

--interval <duration> Telemetry interval (default: 30s)
                     Go duration format: 30s, 1m, 2m30s
```

The script:
1. Detects the CPU architecture (amd64 / arm64)
2. Finds the matching agent binary in the same directory
3. Copies it to `/opt/kvm-center/kvm-center-agent`
4. Writes `/etc/systemd/system/kvm-center-agent.service`
5. Enables and starts the service

---

## Network and Firewall

### Required Connectivity

| Source | Destination | Port | Description |
|---|---|---|---|
| Agents (hvnode01–03) | Server (REDACTED-HOST) | TCP 8090 | Telemetry reporting |
| Server (REDACTED-HOST) | Agents (REDACTED-HOST-11–13) | TCP 8081 | Command routing |
| Browser | nginx webui (REDACTED-HOST) | TCP 8096 | Dashboard access |

### Firewall on the Docker Host

Rocky Linux uses `firewalld` by default:

```bash
firewall-cmd --add-port=8090/tcp --permanent   # API server
firewall-cmd --add-port=8096/tcp --permanent   # Web UI
firewall-cmd --reload
```

### Firewall on Hypervisors

Allow the command port from the server only:

```bash
firewall-cmd --add-rich-rule='rule family="ipv4" \
  source address="REDACTED-HOST" \
  port port="8081" protocol="tcp" accept' --permanent
firewall-cmd --reload
```

Or more permissively:
```bash
firewall-cmd --add-port=8081/tcp --permanent
firewall-cmd --reload
```

---

## Configuration Reference

### Server Environment Variables

Set in `docker-compose.yml` or as environment variables:

| Variable | Default | Description |
|---|---|---|
| `KVM_CENTER_ADDR` | `:8090` | Listen address for the Go server |
| `KVM_CENTER_DATA` | `/var/lib/kvm-center/data.json` | JSON persistence file path |

### Agent CLI Flags

| Flag | Default | Description |
|---|---|---|
| `-server` | `http://REDACTED-HOST:8090` | Control plane URL |
| `-port` | `8081` | Agent command server port |
| `-interval` | `30s` | Telemetry reporting interval |

Environment variable equivalents (override flags):

| Variable | Flag |
|---|---|
| `KVM_CENTER_SERVER` | `-server` |
| `KVM_CENTER_AGENT_PORT` | `-port` |

### nginx Web UI Environment Variables

Set in `docker-compose.yml`:

| Variable | Default | Description |
|---|---|---|
| `KVM_CENTER_SERVER_HOST` | `kvm-center-server` | Hostname nginx proxies /api/ to |
| `KVM_CENTER_SERVER_PORT` | `8090` | Port nginx proxies /api/ to |

---

## Updating

### Server Update

```bash
cd /opt/kvm-center
git pull
docker compose build
docker compose up -d
```

The JSON data file persists across rebuilds via the `kvm-center_kvm-center-data` Docker volume.

### Agent Update

Build new binaries on the docker host, then re-run the deploy loop:

```bash
cd /opt/kvm-center
# Rebuild agent binary
docker build -f Dockerfile.build -t kvm-center-build:latest .
docker create --name kvm-builder kvm-center-build:latest
docker cp kvm-builder:/dist/kvm-center-agent-amd64 ./kvm-center-agent-amd64
docker rm kvm-builder

# Push to each hypervisor
for NODE in REDACTED-HOST-11 REDACTED-HOST-12 REDACTED-HOST-13; do
  scp kvm-center-agent-amd64 root@$NODE:/tmp/
  ssh root@$NODE "cp /tmp/kvm-center-agent-amd64 /opt/kvm-center/kvm-center-agent && \
    systemctl restart kvm-center-agent"
done
```

---

## Data Persistence and Backup

The server persists state to a JSON file in a named Docker volume. Agents re-report all state within 30 seconds of restart, so the data file is more of a warm-start cache than a critical database.

**Backup the volume:**
```bash
docker run --rm \
  -v kvm-center_kvm-center-data:/data \
  -v $(pwd):/backup \
  alpine tar czf /backup/kvm-center-data-$(date +%F).tar.gz /data
```

**Restore:**
```bash
docker compose down
docker run --rm \
  -v kvm-center_kvm-center-data:/data \
  -v $(pwd):/backup \
  alpine tar xzf /backup/kvm-center-data-YYYY-MM-DD.tar.gz -C /
docker compose up -d
```

**Inspect the data file directly:**
```bash
docker exec kvm-center-server cat /var/lib/kvm-center/data.json | python3 -m json.tool
```

---

## Troubleshooting

### Agents not appearing in the UI

1. Check the agent is running on the hypervisor:
   ```bash
   systemctl status kvm-center-agent
   journalctl -u kvm-center-agent -n 30
   ```

2. Verify the agent can reach the server:
   ```bash
   curl http://REDACTED-HOST:8090/health
   ```

3. Check the server received telemetry:
   ```bash
   docker logs kvm-center-server --tail 20
   # Should show: telemetry: REDACTED-HOST-11  host=hvnode01...
   ```

### Commands fail with "agent unreachable"

1. Verify the agent command port is listening:
   ```bash
   ss -tlnp | grep 8081
   ```

2. Test the agent command endpoint from the server:
   ```bash
   curl -s -X POST http://REDACTED-HOST-11:8081/command \
     -H "Content-Type: application/json" \
     -d '{"action":"START","vm_name":"<vm-name>"}'
   ```

3. Check firewall on the hypervisor:
   ```bash
   firewall-cmd --list-all
   ```

### CPU usage always shows 0%

The agent calculates CPU% as a delta between two readings. The first telemetry report always shows 0% because there is no previous reading to compare against. By the second report (30 seconds later), real values will appear.

### Web UI shows "Disconnected"

1. Check nginx is proxying correctly:
   ```bash
   curl -s -o /dev/null -w "%{http_code}" http://REDACTED-HOST:8096/api/health
   # Should return 200
   ```

2. Inspect nginx logs:
   ```bash
   docker logs kvm-center-webui
   ```

3. Verify the server is running:
   ```bash
   docker compose ps
   curl http://localhost:8090/health
   ```

### Port conflict on startup

Check what is using the port:
```bash
ss -tlnp | grep 8090
ss -tlnp | grep 8096
```

Change the port in `docker-compose.yml` if needed and rebuild.
