from fastapi import FastAPI, HTTPException
from fastapi.responses import HTMLResponse
from fastapi.middleware.cors import CORSMiddleware
from pydantic import BaseModel
from typing import List, Dict, Any
import sqlite3
from datetime import datetime
import uvicorn
import os
import re
import requests

# Helper to sanitize strings and remove non-printable characters
def sanitize_string(value):
    if value is None:
        return "Unknown"
    if isinstance(value, bytes):
        value = value.decode('utf-8', 'ignore')
    return re.sub(r'[^\x20-\x7E]', '', str(value))

# Static HTML - NO Python f-strings to avoid syntax errors
UI_HTML = """<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>KVM-Center | Dashboard</title>
    <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="bg-slate-900 text-slate-100 min-h-screen font-sans">
    <nav class="border-b border-slate-800 bg-slate-900/50 backdrop-blur-md sticky top-0 z-10">
        <div class="max-w-7xl mx-auto px-4 h-16 flex items-center justify-between">
            <div class="flex items-center gap-2">
                <span class="text-2xl">🌸</span>
                <h1 class="text-xl font-bold tracking-tight">KVM-Center <span class="text-xs font-normal text-slate-500 ml-2">v1.2.0-STATIC</span></h1>
            </div>
            <div class="flex items-center gap-4">
                <div id="connection-status" class="text-xs px-2 py-1 rounded-full bg-slate-800 text-slate-400">
                    Checking Server...
                </div>
            </div>
        </div>
    </nav>
    <main class="max-w-7xl mx-auto px-4 py-8">
        <section class="mb-12">
            <h2 class="text-sm font-semibold uppercase tracking-wider text-slate-500 mb-4">Hypervisors</h2>
            <div id="host-grid" class="grid grid-cols-1 md:grid-cols-3 gap-4">
                <div class="animate-pulse bg-slate-800 h-24 rounded-xl"></div>
                <div class="animate-pulse bg-slate-800 h-24 rounded-xl"></div>
                <div class="animate-pulse bg-slate-800 h-24 rounded-xl"></div>
            </div>
        </section>
        <section>
            <div class="flex justify-between items-end mb-4">
                <h2 class="text-sm font-semibold uppercase tracking-wider text-slate-500">Virtual Machines</h2>
                <input id="vm-search" type="text" placeholder="Search VMs..." class="bg-slate-800 border border-slate-700 rounded-lg px-3 py-1 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
            </div>
            <div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
                <table class="w-full text-left text-sm">
                    <thead class="bg-slate-700/50 text-slate-400 font-medium">
                        <tr>
                            <th class="px-4 py-3">VM Name</th>
                            <th class="px-4 py-3">Host</th>
                            <th class="px-4 py-3">Status</th>
                            <th class="px-4 py-3 text-right">Resources</th>
                            <th class="px-4 py-3 text-center">Actions</th>
                        </tr>
                    </thead>
                    <tbody id="vm-table-body" class="divide-y divide-slate-700"></tbody>
                </table>
            </div>
        </section>
        <section class="mt-12 p-4 bg-black/50 rounded-xl border border-slate-800 font-mono text-xs text-slate-400">
            <h3 class="text-slate-500 mb-2 uppercase font-bold">System Debug Log</h3>
            <div id="debug-log" class="whitespace-pre-wrap overflow-auto max-h-64">Waiting for data...</div>
        </section>
    </main>
    <script>
        alert("KVM-Center v1.3.0 - JavaScript is RUNNING!");
        const API_BASE = "/api";
        
        function log(msg) {
            const el = document.getElementById('debug-log');
            el.innerText += "\\n[" + new Date().toLocaleTimeString() + "] " + msg;
            el.scrollTop = el.scrollHeight;
        }

        log("JavaScript loaded successfully!");
        log("API Base: " + API_BASE);

        async function updateDashboard() {
            log("Requesting data from server...");
            try {
                const hostsRes = await fetch(API_BASE + "/hosts");
                const vmsRes = await fetch(API_BASE + "/vms");
                
                if (!hostsRes.ok || !vmsRes.ok) {
                    throw new Error("Server returned error: " + hostsRes.status + " / " + vmsRes.status);
                }

                const hosts = await hostsRes.json();
                const vms = await vmsRes.json();

                log("Received " + hosts.length + " hosts and " + vms.length + " VMs");
                document.getElementById('connection-status').textContent = 'Online';
                document.getElementById('connection-status').className = 'text-xs px-2 py-1 rounded-full bg-green-900/30 text-green-400';

                renderHosts(hosts);
                renderVMs(vms);
            } catch (e) {
                log("Error: " + e.message);
                document.getElementById('connection-status').textContent = 'Offline';
                document.getElementById('connection-status').className = 'text-xs px-2 py-1 rounded-full bg-red-900/30 text-red-400';
            }
        }

        function renderHosts(hosts) {
            log("renderHosts called with " + hosts.length + " hosts");
            try {
            const grid = document.getElementById('host-grid');
            if (!hosts || hosts.length === 0) {
                grid.innerHTML = '<div class="col-span-3 text-center py-4 text-slate-500">No hosts reporting...</div>';
                return;
            }
            let html = '';
            for (let i = 0; i < hosts.length; i++) {
                const h = hosts[i];
                log("Host " + i + ": " + JSON.stringify(h));
                const cpu = typeof h.cpu_usage === 'number' ? h.cpu_usage.toFixed(1) : 'N/A';
                const mem = typeof h.mem_usage === 'number' ? h.mem_usage.toFixed(1) : 'N/A';
                const lastSeen = h.last_seen || 'Unknown';
                html += '<div class="bg-slate-800 p-4 rounded-xl border border-slate-700">';
                html += '<div class="flex justify-between items-start mb-3">';
                html += '<span class="font-bold">' + (h.host_id || 'Unknown') + '</span>';
                html += '<span class="text-xs text-slate-500">' + lastSeen + '</span>';
                html += '</div>';
                html += '<div class="grid grid-cols-2 gap-2 text-xs">';
                html += '<div class="flex justify-between"><span class="text-slate-500">CPU</span><span class="font-mono">' + cpu + '%</span></div>';
                html += '<div class="flex justify-between"><span class="text-slate-500">MEM</span><span class="font-mono">' + mem + '%</span></div>';
                html += '</div></div>';
            }
            grid.innerHTML = html;
            log("renderHosts completed");
            } catch (e) {
                log("renderHosts ERROR: " + e.message);
            }
        }

        function renderVMs(vms) {
            log("renderVMs called with " + vms.length + " VMs");
            try {
            const body = document.getElementById('vm-table-body');
            const search = document.getElementById('vm-search').value.toLowerCase();
            
            if (!vms || vms.length === 0) {
                body.innerHTML = '<tr><td colspan="5" class="px-4 py-8 text-center text-slate-500">No VMs found in cluster</td></tr>';
                return;
            }

            let html = '';
            for (let i = 0; i < vms.length; i++) {
                const vm = vms[i];
                if ((vm.name || "").toLowerCase().indexOf(search) === -1) continue;
                
                log("VM " + i + ": " + JSON.stringify(vm));
                const name = vm.name || 'Unknown';
                const host = vm.host_id || 'Unknown';
                const state = (vm.state || 'UNKNOWN').toUpperCase();
                const cpu = vm.cpu || 0;
                const mem = vm.mem || 0;
                
                let actionButtons = '';
                if (state === 'SHUTOFF') {
                    actionButtons = '<button onclick="executeAction(\\'' + vm.id + '\\', \\'START\\')" class="px-2 py-1 bg-green-600 hover:bg-green-500 rounded text-xs">Start</button>';
                } else if (state === 'RUNNING') {
                    actionButtons = '<button onclick="executeAction(\\'' + vm.id + '\\', \\'STOP\\')" class="px-2 py-1 bg-red-600 hover:bg-red-500 rounded text-xs">Stop</button> <button onclick="executeAction(\\'' + vm.id + '\\', \\'RESTART\\')" class="px-2 py-1 bg-slate-600 hover:bg-slate-500 rounded text-xs">Restart</button>';
                } else {
                    actionButtons = '<button class="px-2 py-1 bg-slate-700 rounded text-xs cursor-not-allowed">N/A</button>';
                }

                html += '<tr class="hover:bg-slate-700/30">';
                html += '<td class="px-4 py-3 font-medium">' + name + '</td>';
                html += '<td class="px-4 py-3 text-slate-400">' + host + '</td>';
                html += '<td class="px-4 py-3"><span class="text-xs font-bold">' + state + '</span></td>';
                html += '<td class="px-4 py-3 text-right font-mono text-xs text-slate-400">' + cpu + ' vCPU | ' + Math.round(mem / 1024) + 'MB</td>';
                html += '<td class="px-4 py-3 text-center">' + actionButtons + '</td>';
                html += '</tr>';
            }
            body.innerHTML = html;
            log("renderVMs completed");
            } catch (e) {
                log("renderVMs ERROR: " + e.message);
            }
        }

        async function executeAction(vm_id, action) {
            const btn = event.target;
            const originalText = btn.innerText;
            btn.disabled = true;
            btn.innerText = '...';
            
            try {
                const res = await fetch(API_BASE + "/command", {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({ vm_id: vm_id, action: action })
                });
                const data = await res.json();
                if (data.status === 'ok') {
                    alert('Success: ' + data.message);
                } else {
                    alert('Error: ' + data.message);
                }
            } catch (e) {
                alert('Network error occurred');
            } finally {
                btn.disabled = false;
                btn.innerText = originalText;
                updateDashboard();
            }
        }

        document.getElementById('vm-search').addEventListener('input', updateDashboard);
        setInterval(updateDashboard, 5000);
        updateDashboard();
    </script>
</body>
</html>"""

app = FastAPI(title="KVM-Center Control Plane")

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

DB_PATH = "/data/kvm_center.db"

def init_db():
    with sqlite3.connect(DB_PATH) as conn:
        conn.execute("""
            CREATE TABLE IF NOT EXISTS hosts (
                host_id TEXT PRIMARY KEY,
                last_seen TIMESTAMP,
                cpu_usage REAL,
                mem_usage REAL
            )
        """)
        conn.execute("""
            CREATE TABLE IF NOT EXISTS vms (
                vm_id TEXT PRIMARY KEY,
                host_id TEXT,
                name TEXT,
                state TEXT,
                cpu INTEGER,
                mem INTEGER,
                last_updated TIMESTAMP,
                FOREIGN KEY(host_id) REFERENCES hosts(host_id)
            )
        """)
        conn.commit()

@app.on_event("startup")
def startup_event():
    init_db()
    print("SERVER STARTUP: Loaded UI version v1.2.0-STATIC")

class VMTelemetry(BaseModel):
    name: str
    id: str
    state: str
    cpu: int
    mem: int

class HostTelemetry(BaseModel):
    host_id: str
    cpu_usage: float
    mem_usage: float
    vms: List[VMTelemetry]
    timestamp: str

class CommandRequest(BaseModel):
    vm_id: str
    action: str

@app.get("/", response_class=HTMLResponse)
async def read_index():
    return UI_HTML

@app.post("/api/telemetry")
async def receive_telemetry(data: HostTelemetry):
    try:
        with sqlite3.connect(DB_PATH) as conn:
            host_id = sanitize_string(data.host_id)
            conn.execute(
                "INSERT INTO hosts (host_id, last_seen, cpu_usage, mem_usage) VALUES (?, ?, ?, ?) "
                "ON CONFLICT(host_id) DO UPDATE SET last_seen=excluded.last_seen, cpu_usage=excluded.cpu_usage, mem_usage=excluded.mem_usage",
                (host_id, data.timestamp, data.cpu_usage, data.mem_usage)
            )
            for vm in data.vms:
                vm_id = sanitize_string(vm.id)
                vm_name = sanitize_string(vm.name)
                vm_state = sanitize_string(vm.state)
                conn.execute(
                    "INSERT INTO vms (vm_id, host_id, name, state, cpu, mem, last_updated) VALUES (?, ?, ?, ?, ?, ?, ?) "
                    "ON CONFLICT(vm_id) DO UPDATE SET state=excluded.state, last_updated=excluded.last_updated",
                    (vm_id, host_id, vm_name, vm_state, vm.cpu, vm.mem, data.timestamp)
                )
            conn.commit()
        return {"status": "ok"}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

@app.post("/api/command")
async def execute_command(req: CommandRequest):
    try:
        with sqlite3.connect(DB_PATH) as conn:
            conn.row_factory = sqlite3.Row
            cursor = conn.execute("SELECT host_id FROM vms WHERE vm_id = ?", (req.vm_id,))
            row = cursor.fetchone()
            if not row:
                raise HTTPException(status_code=404, detail="VM not found")
            
            host_id = row['host_id']
            
            host_map = {
                "hvnode01.example.lan": "REDACTED-HOST-11",
                "hvnode02.example.lan": "REDACTED-HOST-12",
                "hvnode03.example.lan": "REDACTED-HOST-13"
            }
            target_ip = host_map.get(host_id, host_id)
            
            agent_url = f"http://{target_ip}:8081/command"
            response = requests.post(agent_url, json={"action": req.action, "vm_id": req.vm_id}, timeout=10)
            
            if response.status_code == 200:
                return response.json()
            else:
                return {"status": "error", "message": f"Agent returned {response.status_code}: {response.text}"}
                
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

@app.get("/api/hosts")
async def list_hosts():
    with sqlite3.connect(DB_PATH) as conn:
        conn.row_factory = sqlite3.Row
        cursor = conn.execute("SELECT * FROM hosts")
        return [dict(row) for row in cursor.fetchall()]

@app.get("/api/vms")
async def list_vms():
    with sqlite3.connect(DB_PATH) as conn:
        conn.row_factory = sqlite3.Row
        cursor = conn.execute("SELECT * FROM vms")
        return [dict(row) for row in cursor.fetchall()]

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8090)
