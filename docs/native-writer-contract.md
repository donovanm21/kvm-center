# Native writer contract: first dependency slice

This document tracks the shared dependency of issues #65 and #81. The first
slice is `internal/nativework`, a dedicated, bounded SQLite journal with synthetic
adapter tests. **It is not wired into agent mutation paths, UI cleanup, migration
or affinity release.** It provides no production writer exclusion and no native
quiescence claim. Existing PR82 command replay/dispatch closure and PR102 affinity
reservations retain their contracts. No live acceptance is authorized here.

## Implemented contract

- A request is permanently bound to its attempt ID, action, exact canonical UUID
  and supported ASCII name, parameter SHA-256 and owning journal session. A
  request retry never returns a new lease; changed bindings refuse. Unsupported
  names/aliases and schema/value/relationship ambiguities refuse.
- `Begin` durably registers a root before returning permission for its registered
  effect. `Child` registers each descendant before it is spawned. A stopped
  parent cannot register a child. Each writer has an independent opaque lease.
- Root/child admission, parent completion and permanent target `Seal` serialize
  through SQLite writer transactions, including separate journal handles. Seal
  rejects subsequent roots and descendants, but does not stop existing work.
- `Complete` invokes the stop observer bound to the owning lease at registration.
  It passes immutable request/attempt/writer/parent/session identity. Only that
  observer's positive cessation observation can persist this writer as stopped.
  There is no public `stopped=true` receipt, operation-status import or timer.
  Completion never completes descendants. A duplicate receipt is idempotent and
  a late old completion cannot resolve a newer attempt.
- A stop observer is trusted implementation code, not evidence by itself. It must
  own the exact native handle/generation and positively exclude all effects in its
  declared writer scope. Nil error must never mean only process return, terminal
  command status, cancellation requested, lost handle, timeout or agent restart.
  This batch supplies synthetic observers only; no production observer is
  qualified. Unsupported native operations must remain unresolved.
- Cancellation records intent only. Restart creates a new journal session without
  taking ownership of existing writers. Old leases cannot transfer to another
  handle; a closed owner cannot complete work. An independently live owner can
  still report its own exact writer, without authorizing replacement ownership.
- Commit acknowledgement uncertainty fences the current handle and returns no
  execution permission or affirmative evidence. Reopening follows actual durable
  records; it does not invent missing observations. All requests, writers and
  fences remain retained; 4096 target/writer limits refuse rather than prune.
- Exact schema and complete bounded row graph validation precede admission and
  evidence. Unknown versions, altered constraints/columns/triggers, orphan rows,
  invalid flags/identities, missing roots and parent cycles refuse. The file is a
  trusted application journal, not a tamper-proof external attestation.

Evidence exposes `target_admission_closed`, `registered_writers_unresolved` and
`registered_writer_drain_verified`. The latter requires permanent closure, at
least one registered writer and positive observations for every registered writer.
It is scoped to that registered set. `native_quiescence_verified` and
`writer_coverage_complete` are always false. Consumers must not substitute this
scoped result for native acceptance, complete writer coverage or placement proof.

No journal reset, automatic recovery lease, reopening of a sealed target, TTL,
pruning or historical-snapshot rollback is provided. A dedicated journal may be
copied only after its owner is closed and all external file writers excluded;
such recovery preserves uncertainty and never certifies later effects absent.
Deleting it, restoring an older copy or using a new path is not permission to act.
Existing application backups, raw legacy BSON/history and pinned rollback artifacts
are untouched. Integration must add its own reviewed backup/provenance binding.

## Remaining dependency slices

1. **Qualified bounded native producer and coverage inventory.** Bind a journal
   lease to an exact native operation handle and generation. Track subprocesses,
   libvirt jobs and asynchronous descendants before effects. Start with bounded
   power operations and the disposable UI adapter's Define/Start/Destroy/Undefine
   and cleanup paths; do not infer descendants stopped from a method returning.
   Existing seams include agent command admission/execution, cancellation and
   watchdog paths, and `internal/nativevalidation.Driver`. Preserve unresolved
   legacy command admissions rather than silently importing them as drained.
2. **Writer exclusion through observation and cleanup/admission.** Register or
   explicitly exclude every writer of the scoped UUID/name/resources, including
   alternative agent routes, UI/watchdog cleanup and out-of-band libvirt writers.
   The prototype Seal covers only journal users; it cannot exclude these other
   writers. Define a request-bound, generation-bound barrier that survives restarts
   and cannot be bypassed by late queued work or a second agent handle.
3. **Live/offline migration coverage and two-endpoint observation.** Register
   transfer, conversion, destination definition/start and source finalization
   workers, including partial failure paths. Under a qualified barrier, obtain
   complete fresh exact-UUID source/destination observations. Success requires
   source absence and destination presence; verified abort requires unchanged
   placement and no residual work. Query/permission errors are never absence.
4. **Versioned consumers and recovery.** Bind producer identity, attempt,
   generation, observation order and coverage scope into an authenticated evidence
   contract. Only then separately review affinity reconciliation and UI ownership
   release. Preserve old reservations and old backup uncertainty. Repeated native
   placement requires a separately reviewed safe barrier transition; this slice's
   permanent seal has no reopen operation.

## Required disposable-lab acceptance (execution remains HOLD)

Use only a separately authorized disposable lab and new synthetic owned fixtures.
Record exact deployed agent/server/libvirt provenance and capabilities first;
source CI is not proof that those binaries are installed. Keep raw private traces
locally; public evidence uses synthetic labels, no addresses or host identifiers.

| Scenario | Required positive evidence and refusal |
| --- | --- |
| Registered root and delayed child | Native handle/generation and every child are correlated to the request/attempt. Root completion leaves the active child unresolved. No cleanup/replacement is admitted. |
| Close versus queued Start/spawn | Race closure against root admission and child spawn. Every effect either has a preceding durable registration or is refused before the native call. Late queued work cannot recreate the fixture after cleanup. |
| Cancellation with active native work | Logical cancellation and terminal status leave the writer unresolved until the exact child/libvirt job and its side effects have positively ceased. No fixed sleep or PID disappearance is accepted as proof. |
| Lost response and overlapping retry | Drop acknowledgements before/after dispatch and persistence. Same-ID replay and changed-attempt/payload reuse cannot redispatch; a new request cannot overlap unresolved native work. |
| Agent/server restart or missing handle | Retain unknown writers, attempt identity and fences. Restart does not assert completion, create replacement ownership or release UI/affinity resources. |
| Late completion | Deliver an old completion after a newer attempt has started where admission is otherwise valid. Only the old writer can change; no descendant or newer attempt is released. |
| Partial offline migration | Leave copy/conversion/definition/finalization work pending on either endpoint. No placement release until all covered native work stops and complete barrier-bound placement observations agree. |
| Competing native writer | Attempt a second agent, alternate API or out-of-band libvirt mutation during observation and cleanup. It must be excluded by the qualified barrier, or evidence must explicitly remain unsupported. |
| Storage/restore failure | Lose commit acknowledgement and restore supported journal/application snapshots under exclusive ownership. Preserve fences, raw originals and unknown work; older snapshots never prove later work absent. |
| Final cleanup/repeated placement | Only after qualified coverage/quiescence evidence, prove exact owned fixture absence and no possible late reappearance. For migration, prove both endpoint placement invariants before separately enabling another move. |

Until these producer, exclusion and consumer gates pass, issues #65/#81 and the
UI validation execution hold remain open. Guest lifecycle/routing and the complete
legacy compatibility inventory remain tracked in #65; this batch changes none of
those semantics. Public release and deployment remain on hold.

## Second slice: tracked direct Virsh mutation, daemon effects unresolved

`TrackVirshMutation` now binds one exact Define/Start/Destroy/Undefine invocation
in the real `Virsh.runRaw` execution path. Untracked mutations refuse before
spawn. The tracker binds the explicit executable/socket/private environment,
owned fixture identity, request/attempt, action and argument/input digest. Define
accepts only the canonical fixture XML for that identity. The capability is
consumed before validation or registration uncertainty and never reset.

A daemon-effect root and direct-process child are durably registered before
`exec.Cmd.Start`. Only the retained exact command handle is waited, once.
`ProcessReceipt.direct_process_reaped` is separate diagnostic evidence of that
handle's reaping after `Wait`, including bounded pipe handling. It is not a stop
observation, does not survive restart, and cannot complete either journal writer.
No PID disappearance, cancellation, successful exit, error output, missing handle,
late response or restart establishes cessation of native effects. A subprocess
may have descendants or the daemon may continue work after its caller exits.
Both writers therefore remain unresolved, permanently blocking another mutation
of that target through this journal. Failed registration never spawns; failed
start still retains the conservative registrations. Cancellation intent is saved
when the invocation returns, where persistence remains available; it never clears
unknown work. If persistence fails, the retained unknown writers still block.

The exact direct-process receipt is the only observation supplied by this slice.
There is still **no qualified daemon-side cessation producer**. This tracking API
is not a supported operational workflow: it cannot safely run automatic cleanup.
Adapter Prepare/Recover and watchdog run/recovery refuse a Virsh driver before
activation, even if a tracker was supplied. This also prevents the earlier opt-in
native acceptance harness from progressing to fixture effects. Missing or hidden readiness capabilities also refuse. Synthetic drivers
explicitly qualify only within isolated test code. Other agent routes and out-of-band writers
are not covered, and native quiescence/complete writer coverage remain false.

### Lab handoff status

**Native acceptance is not ready for execution.** No host action is authorized by
this change. The executable synthetic acceptance uses real local helper processes
through the same execution seam to test registration, mismatches, simultaneous
reuse, cancellation, delayed pipe completion, failed starts/exits, lost responses,
and active-owner restart. It calls no libvirt service.

Before a native lab handoff can be marked READY, the next review must qualify a
request/attempt/generation-bound daemon operation observer, account for descendants
and every competing route, and provide a safe cleanup strategy under that barrier.
In particular, successful `virsh` exit alone must never be mapped to `Complete`.
The lab matrix above remains mandatory; current production paths, affinity
reconciliation, UI execution, deployment and release remain on hold. Retain the
journal and raw originals indefinitely; deleting or replacing it cannot authorize
a retry or cleanup.

## Finite power-operation epochs (synthetic implementation)

`PowerCoordinator` implements operation-specific completion for canonical fixture
Define, Start, Destroy and **inactive-only** Undefine. It uses an application scope
fixed centrally to the supported local libvirt endpoint and domain UUID; Home,
request IDs and journal paths do not choose another lock. Every legacy tracked
Virsh mutation takes the same OS lock and refuses a managed scope. This excludes
these application routes, not unrelated administrators or every production agent
route. No native profile is currently qualified, and `Execute` refuses native
execution through its public constructor. Test-only drivers exercise the state
machine and the actual subprocess execution seam.

A drained PR103 predecessor is handed off by retaining adoption intent first and
then permanently sealing its exact revision. Unknown writers or an unrelated
existing seal refuse adoption. The successor retains an immutable scope marker
and rechecks the predecessor and all history on each operation. It never reopens
old seals, completes old unknown writers, or creates a new authority when a
managed ledger is missing. Changing Home or predecessor journal cannot bypass the
centrally fixed managed scope.

Each operation retains immutable request/attempt, action, full ownership identity,
execution/profile digests and a monotonically increasing epoch. Its phases are:

`registered -> invocation_possible -> native_ack -> observed -> completed`

The domain lock spans the whole sequence. A successful qualified synchronous
native response is persisted before post-observation or completion. Define checks
owned persistent inactive XML; Start checks the supported running state; Destroy
checks persistent inactive state; Undefine requires inactivity before execution
and complete UUID/name absence afterward. Managed-save, autostart, unsupported
XML/flags and ambiguous observations refuse. This proves the named operation in
its declared scope, not guest readiness or universal native quiescence. Successful
completion permits the next distinct request without discarding history.

Recovery is finite and explicit:

- A completed request replays its historical outcome without executing again.
- A retained registration before `invocation_possible` can be recorded aborted;
  another distinct request may proceed.
- A durable `native_ack` permits observation-only recovery under the same domain
  lock. The history records `recovery_observed`, then completion. No old lease is
  reconstructed and no native mutation is retried.
- Missing native acknowledgement, including timeout/cancellation, returns one
  target-scoped `recovery_required` incident. Other domains remain usable. No
  current-state query, sleep, process return or new request clears that incident.
- Partial file replacement, missing history, changed ownership/provenance or an
  inconsistent chain refuses and preserves the files for explicit recovery.

History is logically append-only: atomic replacement retains all prior events,
fsyncs the file and parent directory, and never prunes. The 256-operation bound
refuses at capacity. Canonical scope storage and its predecessor are authoritative;
deleting, moving or restoring stale copies cannot be used to manufacture admission.
STOP, RESTART, power-cycle, migration and production agent routes remain separate
profiles. The existing adapter/watchdog activation guard remains closed.

### Installed-profile dependency and bounded handoff

Read-only provenance identifies libvirt/virsh 10.10 and QEMU 9.1, but the installed
custom prepare/begin hook synchronously contacts an agent handler whose executable
is not tied to verified source. Inherited routing/config behavior is also
unverified. Reference source is not installed proof. This is **not a hook-free
profile**, and successful synthetic tests do not qualify it. Do not inspect secrets,
change the installed hook/agent/security configuration, or run native acceptance
under this change.

The next handoff needs a separately approved controlled fixture profile: either
an isolated test service with pinned hook/handler/config behavior, or an explicitly
reviewed fixture-only hook path. Neither option is applied or authorized here.
Once that scope is approved, pin its exact artifacts and supported normalized XML,
review qualification, and run three bounded owned-fixture
Define/Start/Destroy/Undefine cycles, replay and crash-after-durable-ack recovery.
Unknown-native-ack injection needs an agreed retained-fixture recovery plan first.
Acceptance requires subsequent requests to succeed after verified completion,
uncertainty to remain target-scoped, and final exact fixture absence for completed
cycles. All originals and unresolved incidents remain retained. Native execution,
UI validation, release and deployment remain HOLD pending those dependencies.

The canonical scope also retains an immutable predecessor-journal binding before any legacy registration or managed adoption. A different empty journal cannot replace unresolved legacy evidence. The complete case-sensitive history schema is mandatory: null, omitted, unknown, or aliased fields refuse admission. The scope directory chain is synced before native effects.
