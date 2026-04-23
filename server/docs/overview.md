---
title: Overview
section: Getting started
order: 10
---

# Overview

kvm-center is a control plane for a fleet of KVM hypervisors — it
replaces the "one-host-at-a-time virsh" workflow with an authenticated
API, a web UI, a CLI, and cluster-wide migration.

Each hypervisor runs a small **agent** that reports telemetry, executes
commands, and proxies the VNC console. The **server** (this process)
owns all authentication, authorization, audit, and migration
orchestration. Operators interact through the React SPA or the
`kvmctl` CLI; both use the same HTTP API and the same RBAC model.

## What's in the box

- **Inventory** — hosts, VMs, snapshots, storage pools, guest-agent
  telemetry.
- **Lifecycle** — start / stop / restart / force-off / migrate /
  snapshot / revert / delete.
- **Migrations** — live and offline, encrypted over mTLS with signed
  authorization grants (v5.10 / v5.11).
- **RBAC** — per-permission grants, built-in roles (Administrator /
  Operator / Viewer), group membership, external identity providers
  (LDAP, SAML, OIDC).
- **Audit log** — every mutation recorded with actor, resource,
  outcome, and whether the call came from the web UI, the CLI, or a
  background task.
- **Notifications** — webhook (Discord / Slack / generic) + email
  channels fire on VM crashes, host offline, migration outcomes, TLS
  cert expiry.
- **Metrics** — optional InfluxDB v2 sink with a Grafana dashboard
  template.

## A VM, start to snapshot

A quick tour of the Actions menu on a VM — shutdown, start back up,
and take a snapshot — all without leaving the detail view.

![VM lifecycle: shutdown, start, snapshot](/api/docs/media/02-vm-lifecycle.webp)

## Where to go next

- **[Getting started](/docs/getting-started)** — stand up the stack,
  enroll your first hypervisor.
- **[Architecture](/docs/architecture)** — how the server, agents,
  and hypervisors fit together.
- **[Authentication](/docs/authentication)** — sessions, API tokens,
  external providers.
- **[Migrations](/docs/migrations)** — mTLS channel, signed grants,
  libvirt hook enforcement.
- **[`kvmctl` CLI](/docs/kvmctl)** — scriptable access to the API.
- **[API reference](/docs/api)** — hand-maintained endpoint reference.
