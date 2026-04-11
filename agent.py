import libvirt
import psutil
import requests
import socket
import time
import logging
import json
from datetime import datetime

# Configuration
SERVER_URL = "http://kvm-center-server:8000/api/telemetry"
CHECK_INTERVAL = 10  # seconds
HOST_ID = socket.gethostname()

logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(levelname)s - %(message)s')

def get_vm_info():
    vms = []
    try:
        conn = libvirt.open('qemu:///system')
        domain_ids = conn.listAllDomains()
        for dom_id in domain_ids:
            dom = conn.lookupByName(dom_id) # This is slightly wrong, listAllDomains returns the actual domain objects in newer bindings or IDs. 
            # Let's use a more robust way:
            pass 
    except Exception as e:
        logging.error(f"Libvirt error: {e}")
    return vms

# Correction for libvirt domain listing
def get_vms_robust():
    vms = []
    try:
        conn = libvirt.open('qemu:///system')
        # In python-libvirt, listAllDomains() returns a list of domain objects
        domains = conn.listAllDomains()
        for dom in domains:
            state = dom.info()[0] # state is the first element of the tuple
            state_map = {
                libvirt.VIR_DOMAIN_RUNNING: "RUNNING",
                libvirt.VIR_DOMAIN_PAUSED: "PAUSED",
                libvirt.VIR_DOMAIN_SHUTOFF: "SHUTOFF",
                libvirt.VIR_DOMAIN_CRASHED: "CRASHED",
                libvirt.VIR_DOMAIN_BLOCKED: "BLOCKED",
            }
            vms.append({
                "name": dom.name(),
                "id": dom.UUID(),
                "state": state_map.get(state, "UNKNOWN"),
                "cpu": dom.info()[1], # vcpus
                "mem": dom.info()[2], # max memory
            })
        conn.close()
    except Exception as e:
        logging.error(f"Libvirt error: {e}")
    return vms

def collect_telemetry():
    return {
        "host_id": HOST_ID,
        "cpu_usage": psutil.cpu_percent(),
        "mem_usage": psutil.virtual_memory().percent,
        "vms": get_vms_robust(),
        "timestamp": datetime.utcnow().isoformat()
    }

def main():
    logging.info(f"KVM-Center Agent starting for host {HOST_ID}...")
    while True:
        try:
            data = collect_telemetry()
            logging.info(f"Sending telemetry: {len(data['vms'])} VMs found.")
            # Commented out until server is live
            # requests.post(SERVER_URL, json=data, timeout=5)
        except Exception as e:
            logging.error(f"Telemetry failed: {e}")
        
        time.sleep(CHECK_INTERVAL)

if __name__ == "__main__":
    main()
