# Guest grow protocol v1: synthetic qualification only

The server now retains immutable grow intent and correlates durable guest outcomes. The shipped guest has **no native grow adapter**: every admitted v1 request durably reports `refused_native_unqualified`. There is no filesystem discovery, grow command, UI action, or claim of native completion. Windows does not advertise this protocol. Legacy `grow_filesystem` instructions are explicitly refused; retained legacy `grow_result` records remain uncorrelated history and never acknowledge v1 intent.

## Identity and delivery

`GET /api/guest-grow?uuid=…` and `POST /api/guest-grow` require `vm.edit`. POST accepts exactly `version`, `request_id`, `guest_session`, `target`, and `desired_bytes`. Target requires exact lowercase VM UUID, disk serial, filesystem UUID, filesystem type (`ext4` or `xfs`), and `layout:"whole-disk"`. Desired size is a positive multiple of 4096, at most 2^60 bytes. Partitions, layered storage, guessed device paths, unknown types/fields, missing fields, null values, conflicting aliases and duplicate JSON keys are refused.

A guest session is random and process-local. The server admits new intent only against a unique inventory VM and a v1 session reported within five minutes. It persists `offered` before exposing the request. Retries retransmit the exact immutable request only to its bound session. A restarted server fences recovered queued work; an offered request can be replayed to the same still-running guest. A restarted guest changes session, preventing old offers from executing even when its local journal was restored from a stale copy.

Guest outcomes include request ID, bound session, SHA-256 of the complete canonical request and reported status. The current report envelope carries its fresh session and all retained outcomes, including outcomes from older sessions. The server accepts matching outcomes only for previously offered requests. Duplicate identical outcomes are idempotent; conflicts, unknown requests and mismatches refuse the report. `reported` means only that the guest supplied a correlated outcome. `native_verified` is always false.

## Durable guest journal

The Linux daemon opens a private SQLite journal once at startup. Schema version 1 permits at most 256 permanent records (1 MiB compact JSON); capacity exhaustion refuses new work. Requests sharing a disk serial or filesystem UUID within a VM overlap permanently, even with a new request ID or session. No pruning/reset endpoint exists.

The guest commits a reservation before an adapter can run and commits an outcome before returning it. Retried or recovered reservations never execute again; interrupted reservations report `recovery_required`. Missing/replaced files, malformed schema, same-inode ledger regression, conflicting records and uncertain persistence fence the process. An observed in-memory state floor catches rollback during the process lifetime, including a ledger reread immediately before an adapter call. Synthetic adapters exercise only fake identities and counters.

A future native adapter must hold an exclusive device/identity lease through resolution, revalidation and the grow itself. The adapter interface alone does not provide that lease. It must produce exactly one identity match and prove current filesystem size < desired size <= device capacity. A successful synthetic adapter status is `adapter_reported`, never verified native completion.

## Recovery and remaining gates

JSON/Mongo persistence, archive export, fresh restore and restart retain the server ledger. Candidate transitions cannot remove or rewrite prior requests/outcomes. Mongo rejects unsupported singleton schema and indexes before writes. No TTL operates on these ledgers. Uncertain server persistence blocks offers and export.

Session fencing is not proof that a restored guest journal contains every historical operation. Paired stale backups, cloned identities, external writers and continuity after a fresh process require independently verified recovery provenance before any native adapter is enabled. Preserve original journals, raw BSON and history indefinitely. The pinned old writer remains unable to round-trip all opaque upgraded state; this protocol does not change that rollback limitation.

Before native execution can be considered: independently verify VM identity against the intended domain, unique stable disk serial and filesystem UUID, supported whole-disk ext4/xfs topology, capacity, exclusive identity/execution lease, journal durability across crashes/storage failures, authenticated intent/outcome binding, stale snapshot/clone handling, actual grow/reboot/read/write behavior, and recovery without rerunning an ambiguous operation. The earlier capture-only hook evidence proves no guest or filesystem behavior. Allowing-start enforcement, UI execution, releases and deployments remain HOLD and require separate authorization/evidence.
