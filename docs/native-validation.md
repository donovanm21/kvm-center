# Bounded native validation adapter

This is an opt-in standalone validation adapter, not a production agent mode.
`cmd/kvm-validation-agent` and `internal/nativevalidation` do not invoke agent
startup, collectors, bootstrap, reconciliation, enrollment, key synchronization,
self-update, storage management or retirement. Existing production code and
`agent -validation` fixture behavior remain unchanged.

The adapter accepts signed `/command` START and FORCESTOP requests using the
existing `agentauth` and synchronous `CommandResponse` contracts. It requires
both the exact UUID and name, a request ID, and no parameters. All other routes
and actions fail closed. Signed GET `/validation/inventory` reports only that
UUID's native state as a `TelemetryPayload`. A test harness must feed this
observation into its isolated control plane; command success is not inventory.
No inventory is sent automatically, and no production endpoint is consumed.

The synthetic server test exercises authenticated control-plane APIs, signing,
adapter command execution and telemetry/API state agreement with an in-memory
driver. The opt-in Linux acceptance test substitutes the actual local virsh
driver. Neither establishes production-agent dispatcher/startup coverage,
browser coverage, a guest OS boot, onboarding/decommissioning acceptance, or
release readiness.

## Resource and ownership contract

- Exactly one persistent domain, with a fresh explicit UUIDv4, a name beginning
  `kvmc-validation-`, and a unique 64-character lowercase hex owner marker.
- Fixed 1 vCPU, 256 MiB RAM, BIOS/default x86_64 machine, no disk, NIC, graphics,
  guest agent, passthrough, filesystem, NVRAM or guest storage. No autostart.
  Existing QEMU/firmware only; no installation, pool or network changes.
- Existing explicitly authorized local system libvirt Unix socket, explicit
  virsh executable. No default URI, SSH transport, inherited credentials,
  ACL changes or security/trust changes. Libvirt/QEMU will create their normal
  per-domain definition/runtime/log files; these are part of the guest scope.
- Private, fresh, non-overlapping test state/credential paths; loopback ephemeral
  HTTP listeners. Port 8081 is forbidden. No production state or credentials.
- A 45-second total adapter lifetime starts immediately before definition;
  cancellation stops admission and invokes a separate 15-second cleanup budget.
  Native subprocesses have 3-second contexts and a bounded wait. These are
  deadlines, not a guarantee against an unresponsive kernel/libvirt daemon.
- At most 64 command records; request identity and pending outcome are synced
  before mutation. Successful same-ID retries do not repeat actions. Reusing an
  ID with a different action is refused. Normal restart on a used root is
  refused; recovery only cleans up and never resumes or recreates a guest.

Both UUID and name must be absent before definition. Connection/permission
failures are not interpreted as absence. An ownership journal is synced before
`define`; domain metadata carries its owner marker. After definition, the
adapter fingerprints the inactive XML returned by libvirt, rather than the
un-normalized input. Before mutations and cleanup it rechecks metadata,
configured CPU/memory/device bounds, and that fingerprint. Cleanup verifies
shutoff, undefines only the exact UUID, verifies absence, then records `Clean`.

Concurrent external changes to this guest or private root are unsupported and
prohibited during acceptance. Inactive XML does not prove absence of live-only
hotplug; this adapter is an ownership boundary for an exclusively controlled
fixture, not a sandbox against a privileged concurrent administrator. Ownership
or XML mismatch stops destructive cleanup and leaves the resource for review.

SIGINT/SIGTERM, timeouts and ordinary errors attempt cleanup. An uncertain native
operation or journal failure returns a nonzero runtime result even if cleanup
succeeds. SIGKILL/power loss cannot run cleanup; retained journal-based recovery
is required. Cleanup accepts a private partial `journal.tmp` only after validating
the committed journal and exact manifest identity; it discards that uncommitted
file and never treats its contents as ownership evidence. A missing/corrupt
committed journal, unexpected file or symlink stops recovery.

## Next native phase: procedure, not permission to execute

The code change and synthetic tests are authorized. Native execution remains
paused pending review of this procedure and its exact resource manifest. The
user's disposable-host authorization covers the intended testing purpose;
action-time filesystem/network approval and existing SSH/libvirt access checks
still apply. Do not change permissions, install software, accept changed host
keys or switch routes to evade a denial. No release/deployment is implied.

1. Pin the reviewed public commit and SHA-256 of both artifacts. Build on an
   isolated builder with the pinned Go toolchain; cross-compilation does not
   execute the native test:

   ```sh
   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly -trimpath -buildvcs=false -o kvm-validation-agent ./cmd/kvm-validation-agent
   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -mod=readonly -c -tags=nativevalidation -o native-validation.test ./server
   sha256sum kvm-validation-agent native-validation.test
   ```

2. Reconfirm hvnode04 identity through its existing strict trusted SSH route,
   idle baseline, recovery access, and authorized existing system libvirt socket.
   Confirm the selected UUID and name are absent and no one will modify this
   fixture concurrently. Record, without secrets: commit/artifact hashes,
   fresh UUID/name/owner marker, explicit virsh/socket paths, existing service,
   pool/network/domain baseline and a unique private test base directory.
   The owner marker is an identifier, not an authentication credential.
   Do not infer disposability of unrelated resources from their names.

3. Review/approve the manifest before creating anything on the host. The exact
   filesystem scope is two staged binaries plus one fresh private base containing
   `manifest-root/` (initially empty), `tmp/`, and private evidence. The acceptance
   test adds `manifest-root/agent-state/journal.json`, temporary lock/temp files,
   a temporary synthetic signing secret, and temporary isolated control-plane
   JSON/session/PKI fixtures under `tmp/`. It uses ephemeral loopback listeners;
   no firewall changes or live enrollment. Native guest scope is exactly the
   single domain above, including its libvirt runtime files/logs.

4. After approval and staging/hash verification, substitute only the recorded
   absolute manifest values. Run this exact test selection as the existing
   authorized identity (do not add sudo/group/ACL changes):

   ```sh
   TMPDIR="$APPROVED_BASE/tmp" \
   KVMC_NATIVE_VALIDATE=I_APPROVE_ONE_DISPOSABLE_DOMAIN \
   KVMC_NATIVE_ROOT="$APPROVED_BASE/manifest-root" \
   KVMC_NATIVE_UUID="$APPROVED_UUID" \
   KVMC_NATIVE_NAME="$APPROVED_NAME" \
   KVMC_NATIVE_OWNER="$APPROVED_OWNER_MARKER" \
   KVMC_NATIVE_VIRSH="$APPROVED_VIRSH" \
   KVMC_NATIVE_SOCKET="$APPROVED_LIBVIRT_SOCKET" \
   "$APPROVED_BASE/native-validation.test" \
     -test.run '^TestNativeValidationAcceptanceOwnedDomain$' -test.count=1 -test.timeout=90s -test.v
   ```

   The test creates an isolated in-process control plane using production API
   handlers and a synthetic login/signing identity, defines the one guest,
   reads owned native state, reports it to that control plane, sends API START,
   checks native/API running agreement, sends API FORCESTOP, checks shutoff,
   checks same-request replay and non-owned refusal, then cancels/cleans up.
   This does not launch the production server or agent binaries. No failure
   injection is part of this phase. Capture the exit result and ownership
   journal; retain raw fixture logs privately until sanitized.

5. Require success AND `Clean: true` in the retained journal, verified domain
   absence, and no differences in shared services, networks, pools or volumes.
   Preserve existing recovery artifacts. Stop on any unexpected shared change,
   ownership ambiguity, lost recovery route or cleanup error. A failed run is
   not permission to widen scope or retry destructive operations.

6. If interrupted, first prove the prior validation process is stopped. Preserve
   the journal and exact manifest. Only then may the stale `owner.lock` be
   removed from this test root under the approved recovery scope. Do not remove
   other files to force recovery past a refusal. Run cleanup-only with the same
   manifest (no secret is needed):

   ```sh
   "$APPROVED_BASE/kvm-validation-agent" -allow-disposable-libvirt -cleanup \
     -uuid "$APPROVED_UUID" -name "$APPROVED_NAME" -owner "$APPROVED_OWNER_MARKER" \
     -state-dir "$APPROVED_BASE/manifest-root/agent-state" \
     -virsh "$APPROVED_VIRSH" -libvirt-socket "$APPROVED_LIBVIRT_SOCKET"
   ```

   Recovery never creates/starts a guest. If ownership/configuration checks fail,
   report the residual UUID and exact reason; do not substitute broad virsh
   delete commands. Remove only manifest-owned local test files after evidence
   capture and successful postflight, under the approved cleanup scope.

### Domain absence verification

The adapter does not classify localized virsh error text. After a failed
`dumpxml`, it requires a successful, bounded `list --all --uuid` (UUID lookup)
or `list --all --name` (name lookup) before treating the exact target as absent.
Inventory diagnostics, malformed or duplicate identifiers, command failures,
and timeouts fail closed. Lookup timeout, output overflow, and execution errors
also fail closed without an inventory fallback. Other commands never classify
error text as absence.

This adds a read of local domain identifiers to the fixture-only lookup scope.
Identifiers remain in memory and are omitted from reports and logs. The approved
execution manifest must include this read and confirm unfiltered inventory
visibility on the existing local socket. Successful enumeration cannot detect
ACL-filtered results or reserve identity against concurrent changes: exclusive
fixture ownership and a quiescent validation window remain prerequisites.
No additional host, credentials, VM resources, or lifecycle operations are added.

### Recovery after interrupted normalized definition

Ownership validation now compares the complete parsed XML against the original
empty-guest profile or one explicitly reviewed normalized profile: x86_64
`pc-i440fx-rhel7.6.0`, qemu64 CPU, `/usr/libexec/qemu-kvm`, disabled USB and
balloon, PCI root, exactly one PS/2 mouse and keyboard, and audio id 1 of type
`none`. Identity/owner and 1 vCPU/256 MiB remain exact. Additional elements,
attributes, devices, namespaces, duplicates or external backing fail closed.
Other libvirt normalizations require separate review, not a broader device list.

For an interrupted definition that never executed commands, use **both**
`-cleanup -cleanup-stopped` with the existing private state directory and exact
manifest identity. This mode requires an empty request journal, never defines,
starts or destroys a guest, and refuses any state other than verified shutoff
with autostart disabled. It opens no listener and needs no signing secret. A
pending definition without a fingerprint is checked against the full profile,
then its exact XML fingerprint is durably saved before undefine. Configuration
and state are re-read before removal; any save/read/state/configuration failure
retains the resource and journal. A clean journal cannot adopt an existing guest.
A pinned interrupted recovery can be retried without clearing the fingerprint.

The lab owner must refresh its artifact hashes and independently review the
exact cleanup-only manifest. Retain the original evidence, confirm no competing
process or identity mutation, and verify disappearance and the unchanged shared
baseline before removing only the approved fixture files. Do not erase/edit a
journal, strip devices, substitute broad virsh commands or switch to normal
lifecycle mode to force cleanup. Read checks do not eliminate concurrent-change
races; exclusive fixture ownership remains required. Recovery approval does not
start another native lifecycle test, release, deploy or change credentials.
