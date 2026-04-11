#!/bin/bash
# KVM-Center Agent Onboarding Script - v8 Command Bridge
# Target: Rocky Linux 9

SERVER_IP="REDACTED-HOST"
SERVER_PORT="8085"
AGENT_DIR="/opt/kvm-center-agent"
SERVICE_NAME="kvm-center-agent"

echo "🚀 Starting KVM-Center Agent onboarding (v8 - Command Bridge)..."

# 1. Ensure Pip and System Bindings
echo "📦 Installing system-level bindings..."
if command -v dnf &> /dev/null; then
    echo "🛠️ Enabling CRB and installing python3-libvirt..."
    dnf config-manager --set-enabled crb || true
    dnf install -y python3-libvirt python3-pip python3-devel gcc
else
    echo "❌ Unsupported package manager."
    exit 1
fi

# 2. Create Agent Directory
mkdir -p $AGENT_DIR

# 3. Deploy Agent Script
echo "📝 Deploying agent script..."
cat << 'EOF' > $AGENT_DIR/agent.py
import libvirt
import psutil
import requests
import socket
import time
import logging
from datetime import datetime
from flask import Flask, request, jsonify

SERVER_URL = "http://REDACTED-HOST:9123/api/telemetry"
COMMAND_PORT = 8081
CHECK_INTERVAL = 10
HOST_ID = socket.gethostname()

logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(levelname)s - %(message)s')

# --- Command Server (Internal) ---
app = Flask(__name__)

@app.route('/command', methods=['POST'])
def handle_command():
    data = request.json
    action = data.get('action')
    vm_id = data.get('vm_id')
    
    logging.info(f"Received command {action} for VM {vm_id}")
    
    try:
        conn = libvirt.open('qemu:///system')
        dom = conn.lookupByID(vm_id)
        
        if action == 'START':
            dom.create()
        elif action == 'STOP':
            dom.destroy() # Hard stop
        elif action == 'RESTART':
            dom.destroy()
            time.sleep(2)
            dom.create()
        elif action == 'SHUTDOWN':
            dom.shutdown()
        else:
            return jsonify({"status": "error", "message": f"Unsupported action: {action}"}), 400
            
        conn.close()
        return jsonify({"status": "ok", "message": f"Successfully executed {action}"})
    except Exception as e:
        logging.error(f"Command execution failed: {e}")
        return jsonify({"status": "error", "message": str(e)}), 500

# --- Telemetry Logic ---
def safe_decode(value):
    if value is None: return "Unknown"
    if isinstance(value, bytes): return value.decode('utf-8', 'ignore')
    return str(value)

def get_vms():
    vms = []
    conn = None
    try:
        conn = libvirt.open('qemu:///system')
        if conn is None: return vms
        domains = conn.listAllDomains()
        for dom in domains:
            try:
                info = dom.info()
                state_map = {
                    libvirt.VIR_DOMAIN_RUNNING: "RUNNING",
                    libvirt.VIR_DOMAIN_PAUSED: "PAUSED",
                    libvirt.VIR_DOMAIN_SHUTOFF: "SHUTOFF",
                    libvirt.VIR_DOMAIN_CRASHED: "CRASHED",
                    libvirt.VIR_DOMAIN_BLOCKED: "BLOCKED",
                }
                vms.append({
                    "name": safe_decode(dom.name()),
                    "id": safe_decode(dom.UUID()),
                    "state": state_map.get(info[0], "UNKNOWN"),
                    "cpu": info[1],
                    "mem": info[2],
                })
            except Exception as dom_e:
                logging.error(f"Error processing domain: {dom_e}")
        conn.close()
    except Exception as e:
        logging.error(f"Libvirt connection error: {e}")
    return vms

def telemetry_loop():
    while True:
        try:
            vm_list = get_vms()
            data = {
                "host_id": HOST_ID,
                "cpu_usage": psutil.cpu_percent(),
                "mem_usage": psutil.virtual_memory().percent,
                "vms": vm_list,
                "timestamp": datetime.utcnow().isoformat()
            }
            requests.post(SERVER_URL, json=data, timeout=5)
        except Exception as e:
            logging.error(f"Telemetry loop failed: {e}")
        time.sleep(CHECK_INTERVAL)

if __name__ == "__main__":
    import threading
    # Start telemetry in a background thread
    t = threading.Thread(target=telemetry_loop, daemon=True)
    t.start()
    # Start command server in main thread
    app.run(host='0.0.0.0', port=COMMAND_PORT)
EOF

# 4. Install remaining Python requirements
echo "🐍 Installing Python dependencies..."
python3 -m pip install --no-cache-dir requests psutil flask

# 5. Setup Systemd Service
echo "⚙️ Configuring systemd service..."
cat << EOF > /etc/systemd/system/$SERVICE_NAME.service
[Unit]
Description=KVM-Center Node Agent
After=network.target libvirtd.service

[Service]
ExecStart=/usr/bin/python3 $AGENT_DIR/agent.py
Restart=always
User=root
Group=libvirt

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable $SERVICE_NAME
systemctl restart $SERVICE_NAME

echo "✅ Onboarding complete! Agent is running as $SERVICE_NAME on port $COMMAND_PORT."
