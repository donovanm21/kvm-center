---
title: Agent gRPC channel
section: Reference
order: 85
---

# Agent gRPC channel (v5.27)

kvm-center's control plane serves a gRPC endpoint alongside the HTTP
REST API. The two transports are complementary:

- **HTTP REST** stays authoritative for telemetry ingest, setup,
  inventory CRUD, everything the SPA consumes, and everything
  `kvmctl` drives. Synchronous request/response over TLS on `:8190`.
- **gRPC** covers signals that only make sense as streams — long-
  running migrations where the operator wants real-time progress,
  future live VM metrics, and future console session lifecycle.

Both listeners share the same TLS cert in production deployments;
gRPC binds `:8095` by default. Set `KVM_CENTER_GRPC_ADDR=` (empty)
to disable.

## Schema

Source of truth: [`proto/agent.proto`](https://github.com/donovanm21/kvmcenter/blob/main/proto/agent.proto).
Generated Go code is committed at `gen/agentpb/` so nobody has to
run `protoc` to build from source.

```proto
service AgentService {
  rpc StreamMigrationProgress(stream MigrationProgress) returns (Ack);
  rpc StreamVMMetrics(stream VMMetricSample) returns (Ack);        // stub
  rpc OpenConsoleSession(ConsoleRequest) returns (ConsoleResponse); // stub
}
```

## StreamMigrationProgress (v5.27.0 — implemented)

The source-side agent opens one stream per in-flight migration and
pushes `MigrationProgress` frames as `virsh migrate --verbose` reports
percentage. The control plane updates the matching `MigrationRecord`
in real time, so the Migrations page can show per-stage progress
instead of waiting for the sync HTTP response.

Benefits observed in the v5.25.1 stuck-migration incident: had this
stream been wired, the operator would have seen progress frames
continuing after the server restart window and wouldn't have needed
to stop the VM or manually reconcile the Mongo record.

Caveats the boot reconciler still covers:

- If the gRPC frame arrives AFTER the server has restarted and
  reloaded from Mongo, the record may be missing a `running` entry
  yet — the frame is dropped with a log line, and the reconciler
  finalises the record once telemetry confirms the VM's location.
- A terminal frame (`phase="complete"` or `phase="failed"`) fans out
  through `finalizeMigration`, same as the synchronous HTTP reply.
  Double-finalisation is safe: the second call is a no-op.

## StreamVMMetrics / OpenConsoleSession (stubs)

Defined in the proto so the schema grows compatibly, but the server
currently returns `Unimplemented`. Wiring is tracked on the roadmap
under "Hybrid gRPC for agent hot paths".

## Security

The v5.27 foundation binds plaintext by default and is restricted
to the management VLAN. A follow-up will front the gRPC listener
with the same TLS cert the HTTPS endpoint uses and validate the
caller's client cert against the hypervisor cert list so a stolen
cert can't impersonate a different host.

## Auditing

gRPC RPCs don't yet append to the `audit_events` ring. Migration
outcomes are still audited via the existing HTTP command path. When
more services move to gRPC, they'll gain audit coverage in a
dedicated interceptor (one entry per terminal RPC, not per frame).
