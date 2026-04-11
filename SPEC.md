# KVM-Center Technical Specification

## Overview
KVM-Center is a centralized management plane for a fleet of KVM hypervisors. It mimics the core functionality of VMware vCenter by providing a single-pane-of-glass WebUI to manage VM lifecycles and host health.

## Architecture
- **Control Plane (Server):** Dockerized FastAPI application with a SQLite backend.
- **Node Agent:** Lightweight Python/Go service on each hypervisor interacting with `libvirt`.
- **Communication:** REST API (Agent -> Server for telemetry, Server -> Agent for commands).

## API Contracts

### 1. Telemetry (Agent -> Server)
`POST /api/telemetry`
- **Payload:**
    - `host_id`: String
    - `cpu_usage`: Float
    - `mem_usage`: Float
    - `vms`: Array of `{ name, id, state, cpu, mem, disk }`
    - `timestamp`: ISO8601

### 2. Commands (Server -> Agent)
`POST /agent/command`
- **Payload:**
    - `action`: Enum (`START`, `STOP`, `RESTART`, `SNAPSHOT`, `MIGRATE`)
    - `vm_id`: String
    - `params`: Object (e.g., snapshot name)

## Feature Set
- **Host Management:** Registration, health checks, resource overview.
- **VM Lifecycle:** Power operations, status monitoring.
- **Inventory:** Global search and filter of all VMs.
- **Storage:** View disk paths and utilization.

## Deployment
- **Server:** Docker Compose.
- **Agent:** Systemd service on Rocky Linux/Ubuntu hosts.
