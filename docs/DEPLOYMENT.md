# Deployment Guide

## Control Plane Server Deployment

### Option A: Docker (Recommended)

```bash
# Copy files to server
scp -r kvm-center/ root@<server-ip>:/opt/kvm-center/

# Deploy
cd /opt/kvm-center
docker compose up -d

# Verify
docker ps | grep kvm-center
curl http://localhost:8090/api/hosts
```

### Option B: Direct Python

```bash
# Install dependencies
pip install fastapi uvicorn pydantic requests psutil python-libvirt

# Run server
cd /opt/kvm-center
python server.py
```

## Agent Deployment

### Automated (Recommended)

From control plane server:

```bash
cd /opt/kvm-center

# Deploy to all nodes
for host in hvnode01 hvnode02 hvnode03; do
    scp onboard_agent.sh root@$host:/tmp/
    ssh root@$host "/tmp/onboard_agent.sh"
done
```

### Manual Deployment

On each hypervisor:

```bash
# 1. Install system packages
dnf install -y python3-libvirt python3-pip python3-devel gcc

# 2. Create agent directory
mkdir -p /opt/kvm-center-agent
cd /opt/kvm-center-agent

# 3. Copy agent.py
# (scp or curl from control plane)

# 4. Install Python dependencies
pip3 install requests psutil flask

# 5. Create systemd service
cat > /etc/systemd/system/kvm-center-agent.service << 'EOF'
[Unit]
Description=KVM-Center Node Agent
After=network.target libvirtd.service

[Service]
ExecStart=/usr/bin/python3 /opt/kvm-center-agent/agent.py
Restart=always
User=root
Group=libvirt

[Install]
WantedBy=multi-user.target
EOF

# 6. Enable and start
systemctl daemon-reload
systemctl enable --now kvm-center-agent

# 7. Verify
systemctl status kvm-center-agent
journalctl -u kvm-center-agent -f
```

## Network Requirements

| Source | Destination | Port | Protocol | Purpose |
|--------|-------------|------|----------|---------|
| Browser | Server | 8090 | TCP | Web UI access |
| Server | Agents | 8081 | TCP | Command execution |
| Agents | Server | 8090 | TCP | Telemetry reporting |

## Firewall Configuration

### On Control Plane Server:
```bash
firewall-cmd --add-port=8090/tcp --permanent
firewall-cmd --reload
```

### On Hypervisor Nodes:
```bash
# Allow inbound from control plane
firewall-cmd --add-port=8081/tcp --permanent
firewall-cmd --reload

# Or restrict to control plane IP only
firewall-cmd --add-rich-rule='rule family="ipv4" source address="<control-plane-ip>" port port="8081" protocol="tcp" accept' --permanent
firewall-cmd --reload
```

## Verification Steps

1. **Server Health:**
   ```bash
   curl http://localhost:8090/api/hosts
   curl http://localhost:8090/api/vms
   ```

2. **Agent Connectivity:**
   ```bash
   # From control plane
   curl http://<agent-ip>:8081/command \
     -X POST \
     -H "Content-Type: application/json" \
     -d '{"vm_id": "test", "action": "START"}'
   ```

3. **Web UI:**
   Open browser to `http://<server-ip>:8090`

## Upgrading

### Server Upgrade:
```bash
cd /opt/kvm-center
git pull  # or copy new files
docker compose down
docker compose up -d --build
```

### Agent Upgrade:
```bash
# On each hypervisor
systemctl stop kvm-center-agent
# Replace agent.py
systemctl start kvm-center-agent
```

## Backup & Restore

### Database Backup:
```bash
docker exec kvm-center-server cp /data/kvm_center.db /backup/kvm_center-$(date +%F).db
```

### Restore:
```bash
docker cp kvm_center-YYYY-MM-DD.db kvm-center-server:/data/kvm_center.db
docker compose restart
```

## Troubleshooting

### Common Issues:

1. **Agents can't reach server:**
   - Check SERVER_URL in agent.py
   - Verify firewall allows port 8090
   - Test: `curl http://<server>:8090/api/hosts`

2. **Commands fail with "No route to host":**
   - Server needs host networking or proper routing
   - Check firewall on hypervisors (port 8081)
   - Verify agent is listening: `netstat -tlnp | grep 8081`

3. **UUID errors in database:**
   - Wipe database: `docker exec kvm-center-server rm /data/kvm_center.db`
   - Restart server to recreate tables
   - Wait for agents to repopulate

4. **Port conflicts:**
   - Check: `netstat -tlnp | grep <port>`
   - Change port in server.py or docker-compose.yml

---

For additional support, check the main README.md or review agent logs:
```bash
journalctl -u kvm-center-agent --since "10 minutes ago"
```
