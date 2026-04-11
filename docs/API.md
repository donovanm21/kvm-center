# API Reference

The KVM Center server exposes a JSON REST API on port 8090. The nginx web UI proxies all `/api/` paths to this port, so browsers interact with the API through port 8096.

All request and response bodies are JSON. All timestamps are ISO 8601 (UTC).

---

## Endpoints

### `GET /health`

Liveness probe. Returns immediately without touching the store.

**Response `200`:**
```json
{ "status": "ok" }
```

---

### `POST /api/telemetry`

Called by agents every 30 seconds to report host and VM state. Creates the host record if it does not exist; updates it if it does. Replaces the VM and storage pool lists for that host atomically.

**Request body:**
```json
{
  "host_id":     "hvnode01.example.lan",
  "ip":          "REDACTED-HOST-11",
  "agent_port":  8081,
  "os":          "Rocky Linux 9.7 (Blue Onyx)",
  "kernel_ver":  "5.14.0-427.13.1.el9_4.x86_64",
  "libvirt_ver": "10.0.0",
  "cpu_usage":   12.4,
  "cpu_count":   32,
  "mem_usage":   62.6,
  "mem_total_mb": 64220,
  "mem_free_mb":  24016,
  "uptime":      "5d 3h 12m",
  "timestamp":   "2026-04-11T21:16:02Z",
  "vms": [
    {
      "uuid":         "d7c0b9e4-1234-5678-abcd-ef0123456789",
      "name":         "Bastion",
      "host_id":      "hvnode01.example.lan",
      "state":        "running",
      "cpu_count":    2,
      "cpu_usage":    3.4,
      "mem_total_mb": 6144,
      "mem_usage_mb": 4096,
      "disk_path":    "/var/lib/libvirt/images/bastion.qcow2",
      "disk_usage_gb": 18.5,
      "disk_total_gb": 500.0,
      "networks": [
        {
          "interface": "vnet0",
          "mac":       "52:54:00:ab:cd:ef",
          "bridge":    "br0",
          "rx_bytes":  1048576,
          "tx_bytes":  524288
        }
      ]
    }
  ],
  "storage": [
    {
      "name":          "images",
      "host_id":       "hvnode01.example.lan",
      "path":          "/var/lib/libvirt/images",
      "capacity_gb":   499.8,
      "allocation_gb": 123.0,
      "available_gb":  376.8,
      "state":         "running",
      "pool_type":     "dir"
    }
  ]
}
```

**Fields:**

| Field | Type | Required | Notes |
|---|---|---|---|
| `host_id` | string | yes | Unique identifier; typically the FQDN from `hostname` |
| `ip` | string | no | Derived from `X-Forwarded-For` or `RemoteAddr` if empty |
| `agent_port` | int | no | Defaults to 8081 if 0 |
| `timestamp` | string | no | Defaults to server receive time if empty |
| `vms` | array | no | Full replacement; empty array clears all VMs for this host |
| `storage` | array | no | Full replacement for storage pools |

**VM `state` values:** `running`, `stopped`, `paused`, `crashed`, `unknown`

**Response `200`:**
```json
{ "status": "ok" }
```

**Response `400`:** Invalid JSON in request body.

---

### `GET /api/hosts`

Returns all known hosts with current status. Hosts whose `last_seen` is more than 2 minutes ago are returned with `status: "offline"`.

**Response `200`:**
```json
[
  {
    "id":           "hvnode01.example.lan",
    "ip":           "REDACTED-HOST-11",
    "agent_port":   8081,
    "cpu_usage":    12.4,
    "cpu_count":    32,
    "mem_usage":    62.6,
    "mem_total_mb": 64220,
    "mem_free_mb":  24016,
    "vm_count":     7,
    "status":       "online",
    "last_seen":    "2026-04-11T21:16:02Z",
    "os":           "Rocky Linux 9.7 (Blue Onyx)",
    "kernel_ver":   "5.14.0-427.13.1.el9_4.x86_64",
    "libvirt_ver":  "10.0.0",
    "uptime":       "5d 3h 12m"
  }
]
```

Returns `[]` if no hosts have reported.

---

### `GET /api/hosts/{id}`

Returns a single host by its `host_id` (URL-encoded if necessary).

**Example:** `GET /api/hosts/hvnode01.example.lan`

**Response `200`:** Same shape as a single element from `GET /api/hosts`.

**Response `404`:**
```json
{ "error": "host not found" }
```

---

### `GET /api/vms`

Returns virtual machines. Optionally filter by host or search term.

**Query parameters:**

| Parameter | Description |
|---|---|
| `host` | Filter to a single host by `host_id` |
| `search` | Case-insensitive substring match against VM name, UUID, or host ID |

**Examples:**
```
GET /api/vms
GET /api/vms?host=hvnode01.example.lan
GET /api/vms?search=bastion
GET /api/vms?host=hvnode01.example.lan&search=llm
```

**Response `200`:**
```json
[
  {
    "uuid":          "d7c0b9e4-1234-5678-abcd-ef0123456789",
    "name":          "Bastion",
    "host_id":       "hvnode01.example.lan",
    "state":         "running",
    "cpu_count":     2,
    "cpu_usage":     3.4,
    "mem_total_mb":  6144,
    "mem_usage_mb":  4096,
    "disk_path":     "/var/lib/libvirt/images/bastion.qcow2",
    "disk_usage_gb": 18.5,
    "disk_total_gb": 500.0,
    "networks": [
      {
        "interface": "vnet0",
        "mac":       "52:54:00:ab:cd:ef",
        "bridge":    "br0",
        "rx_bytes":  1048576,
        "tx_bytes":  524288
      }
    ]
  }
]
```

Returns `[]` if no VMs match.

---

### `GET /api/storage`

Returns storage pools. Optionally filter by host.

**Query parameters:**

| Parameter | Description |
|---|---|
| `host` | Filter to a single host by `host_id` |

**Response `200`:**
```json
[
  {
    "name":          "images",
    "host_id":       "hvnode01.example.lan",
    "path":          "/var/lib/libvirt/images",
    "capacity_gb":   499.8,
    "allocation_gb": 123.0,
    "available_gb":  376.8,
    "state":         "running",
    "pool_type":     "dir"
  }
]
```

Returns `[]` if no pools exist.

---

### `POST /api/command`

Routes a lifecycle command to the agent on the specified host. The server looks up the agent's IP and port from the store and forwards the request. Blocks until the agent responds (15-second timeout).

**Request body:**
```json
{
  "host_id":  "hvnode01.example.lan",
  "vm_name":  "Bastion",
  "vm_uuid":  "d7c0b9e4-1234-5678-abcd-ef0123456789",
  "action":   "STOP",
  "params":   {}
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `host_id` | string | yes | Must match a known host |
| `vm_name` | string | yes* | Name used for virsh call; preferred over UUID |
| `vm_uuid` | string | no | Used as fallback if `vm_name` is empty |
| `action` | string | yes | See action table below |
| `params` | object | no | Action-specific parameters |

**Supported actions:**

| Action | Description | Required params |
|---|---|---|
| `START` | Power on a stopped VM | — |
| `STOP` | Graceful ACPI shutdown | — |
| `FORCESTOP` | Hard power off | — |
| `RESTART` | Send reboot to running VM | — |
| `SNAPSHOT` | Create a VM snapshot | `name` (string, optional — auto-generated if absent) |
| `MIGRATE` | Live-migrate to another host | `destination` (string, required — IP or hostname) |

**SNAPSHOT example:**
```json
{
  "host_id": "hvnode01.example.lan",
  "vm_name": "Bastion",
  "action":  "SNAPSHOT",
  "params":  { "name": "pre-upgrade-2026-04-11" }
}
```

**MIGRATE example:**
```json
{
  "host_id": "hvnode01.example.lan",
  "vm_name": "Bastion",
  "action":  "MIGRATE",
  "params":  { "destination": "REDACTED-HOST-12" }
}
```

**Response `200` (success):**
```json
{
  "success": true,
  "message": "Domain Bastion is being shut down"
}
```

**Response `500` (virsh error):**
```json
{
  "success": false,
  "message": "error: Failed to get domain 'Bastion'"
}
```

**Response `400`:** Missing or invalid request body, unknown action.

**Response `404`:** `host_id` not found in the store.

**Response `502`:** Agent unreachable (agent down, firewall, wrong port).

---

## Agent Endpoint

The agent itself also exposes a single endpoint for the server to call. This is **not** intended to be called directly by browsers.

### `POST /command` (agent port 8081)

```json
{
  "action":   "START",
  "vm_name":  "Bastion",
  "vm_uuid":  "d7c0b9e4-...",
  "params":   {}
}
```

Response shape is identical to `POST /api/command`.

### `GET /health` (agent port 8081)

```json
{ "status": "ok" }
```

---

## Error Format

All error responses use this shape:

```json
{ "error": "human readable description" }
```

## CORS

The server sets `Access-Control-Allow-Origin: *` on all responses, so the API can be called directly from any browser origin during development. In production, requests go through nginx which adds its own headers.
