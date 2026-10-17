# Durable command admission and bounded power closure

The agent extends the existing atomic request claim with a permanent admission ledger. Each request ID binds the submitted action, UUID/name and a canonical SHA-256 parameter digest before any cached or running response. Resolved native identity is recorded separately. Outcome details may expire; the ID never becomes new dispatch permission. Existing operation rows are imported conservatively with worker completion unconfirmed. Admissions without a verified resolved identity block drain globally until an owning executor confirms return; imported legacy rows cannot acquire that proof from a terminal status. This intentionally prevents a legacy upgrade from claiming closure merely because old operations look finished. A separate reviewed compatibility/recovery procedure is required; no clearing API is provided. History already deleted before upgrade cannot be recovered by this migration.

A readable durable store is required for `/command` mutations. Legacy calls without request IDs receive an agent-generated ID in `X-Request-Id`; callers must reuse it to retry. Retrying without that ID remains a new request. VM command actions resolve and verify UUID/name for a new admission, then synchronous power actions use the stable UUID. Other handlers retain the verified canonical name because migration and artifact naming depend on it; they do not contribute confirmed power-drain evidence. Completed replays do not inspect today's native domain, so they continue to work after removal or rename.

Cancellation is a request, not completion. The agent retains the child PID and leaves completion time unset while its executor is active. The server preserves pending reconciliation when the response says `cancellation_requested`. Only the owning executor records `worker_returned`; a failed durable write leaves that fact unconfirmed. Boot reconciliation or a logical terminal task does not create this evidence.

## Permanent target command closure

`CLOSE_VM_COMMANDS` travels through the existing authenticated command route and requires the existing VM-delete permission at the server. It resolves an exact UUID/name pair and permanently closes admission for either identifier in the agent database. It does not stop, delete or modify the native domain. There is no reopen/reset endpoint: never reuse a closed fixture UUID or name. This is an irreversible product-state change and is intended for explicitly owned disposable validation identities, not normal fleet power management.

Closure persists before checking drain; it does not wait under the admission lock. Already-admitted commands may still execute. Late new-ID and no-ID admissions are refused. A closure response only confirms that new admission is closed. It does not authorize cleanup.

The existing read-only `/command-outcome?request_id=...` response has additive `dispatch` evidence:

- `durable`: a persistent admission exists in this agent database.
- `worker_returned`: the owning executor recorded its return.
- `cancellation_requested`: cancellation was requested independently of completion.
- `target_admission_closed`: this exact resolved UUID/name has a permanent fence.
- `unresolved_admissions`: matching work whose worker return is unconfirmed, or whose action is outside the supported synchronous power set.
- `power_dispatch_drained`: admission is closed and no such work remains.
- `native_quiescence_verified`: always false; this endpoint cannot prove native quiescence.

After outcome-detail expiry, the endpoint can return ledger-only evidence with logical `outcome: unknown`. Never reinterpret that as successful command execution. Missing/unreadable evidence is not proof of absence. Restarts preserve unresolved admissions and fences; restoring an older database or reverting to an older binary would invalidate these guarantees and is not an approved rollback procedure.

## Scope and cleanup prerequisites

The initial guarantee covers only `/command` synchronous START, STOP, FORCESTOP and RESTART dispatch. Other `/command` actions also undergo canonical target checks, but make target drain unconfirmed; other mutation routes, external virsh callers, configuration/hotplug writers and independently continuing libvirt jobs require separate exclusion/verification. A returned CLI process is not proof that libvirt-side work has stopped.

For a bounded fixture, require permanent closure and confirmed dispatch drain, then independently verify native stopped state and final UUID/name absence before releasing ownership/evidence. Do not infer safety from a task row, toast, cache TTL, HTTP timeout or arbitrary delay. Preserve the journal and reserve the target while any evidence is uncertain. No queue clearing, agent restart or ad hoc ownership reset substitutes for proof.

This source change does not establish deployed provenance, authorize deployment, or release the current UI validation hold. A compatible upgrade/rollback plan and exact deployed server/agent provenance remain separate gates. Synthetic tests exercise concurrency, payload binding, retention/restart, closure/Start interleavings, cancellation while active, write failures and native identity mismatch without a live hypervisor.
