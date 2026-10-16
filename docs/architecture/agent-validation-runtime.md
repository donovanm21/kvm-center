# Fixture-only agent validation runtime

Status: bounded first implementation. Native agent behavior is unchanged.

The agent has production state, background synchronization and host retirement
operations that are shared across a host. A second command port alone does not
isolate another process. Validation therefore enters a separate startup path
before reading cached credentials, initializing native collectors, configuring
self-update, reconciling prior native operations or starting workers.

Run the validation binary only with separately owned temporary directories:

```sh
mkdir -m 700 ./validation-state ./validation-pki
kvm-center-agent -validation \
  -validation-state-dir "$PWD/validation-state" \
  -validation-pki-dir "$PWD/validation-pki" \
  -validation-listen 127.0.0.1:0
```

The listener's actual allocated address is logged and returned by `GET /health`.
Only that route and `GET /validation/inventory` are available. The inventory is
explicitly labeled synthetic and empty, with identity `validation-fixture`.
Other routes return unsupported; this includes retirement, self-update, storage,
console, migration and VM operations. No outgoing telemetry, control-plane
connection, package installation, host configuration or credential generation
occurs. The PKI directory must remain empty; it is reserved for future explicit
fixture support and is not an authentication or enrollment implementation.

State and PKI roots must be existing private absolute directories, must not be
symlinks or overlap each other, and must not overlap host configuration/state
roots. Validation owns only `operations.db` and its SQLite sidecars under its
state root. It takes an exclusive `validation.lock`; unexpected entries,
symlinked state files, corrupt databases or another owner fail startup before
listening. The operation store is never assigned to native dispatch or reconciled
against libvirt. Directories must be controlled exclusively by the test runner;
this harness is not a sandbox against another process concurrently modifying
those directories. A crash can leave a lock: verify its owning process is stopped
before manually removing that lock. The harness never recursively deletes roots.

Validation rejects production `-server`, `-port`, and `-interval` flags and
inherited `KVM_AGENT_OPS_DB`, `KVM_CENTER_SERVER`, `KVM_CENTER_GRPC_ADDR`,
`KVM_CENTER_SERVER_CA`, and `KVM_CENTER_AGENT_PORT` settings. Production does not
accept validation path flags without `-validation`. No credentials or global
state paths are silently reused. Only a literal loopback bind is accepted;
wildcards and names are rejected. On shutdown the owned HTTP listener and
operation store are closed. Port zero is reported using the actual bound port.

## Decision and alternatives

Keep the existing native startup and add a small validation entrypoint and mux.
This is simpler to review than scattering mode checks throughout every command,
and avoids a broad refactor before native acceptance coverage exists. A
privileged container or a different port would not prevent host-wide effects.
A maintenance swap is a different operational choice requiring its own approved
interruption and rollback plan.

The path configuration is intentionally limited to validation in this batch.
Future credential fixtures, telemetry and native resource adapters must remain
explicit capabilities with their own reviewed ownership and cleanup semantics.
Do not point this mode at a production control plane or interpret fixture tests
as onboarding, native lifecycle, decommissioning, migration, security or release
acceptance. Existing authentication and trust behavior is not changed or waived.

## Validation and rollback

Tests exercise configuration rejection, native route exclusion, original-file
sentinels, corrupt-state failure, duplicate ownership, actual loopback port,
clean shutdown/reopen and the real main entrypoint in a child process. CI uses
synthetic temporary resources only. Stop the validation process before removing
its owned temporary files. Reverting this feature requires no production schema
migration; production startup defaults and artifact versions are unchanged.
