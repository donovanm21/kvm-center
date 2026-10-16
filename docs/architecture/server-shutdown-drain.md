# Bounded server shutdown and persistence drain

The optional `-graceful-shutdown` flag enables one coordinator for SIGTERM,
SIGINT, the admin restart request and managed HTTP listener errors. It has a
20-second total budget. Server Compose definitions allow 30 seconds before
forced termination. The flag is deliberately not enabled by those definitions.

## Deployment prerequisite and compatibility

Enable only after establishing exclusive writer ownership of the backend and
application files. In particular, stop and verify the old process has exited
before external seed/import/restore operations. A Store that loaded successfully
cannot detect another writer replacing the database later. A final checkpoint
could overwrite that external state. The startup load gate still prevents a
failed initial load from writing an empty snapshot, but is not a writer lease.
Existing deployments retain immediate signal termination without this flag.
No running server gains this behavior from a source update alone.

The implementation retains Store snapshots, private persistence fields,
serialized backend saves, dirty fingerprints, full retry after partial errors,
and the checked acknowledgements used by command resolution. It does not add a
second persistence mechanism or turn Mongo saves into transactions.

## Drain sequence and results

1. Close request admission, report HTTP 503 for new requests, and cancel the
   server context. Existing HTTP handlers retain their normal error/result
   semantics. Streaming RPCs are stopped; hijacked console peers close and both
   copy goroutines join. Cancellation does not prove that an operation already
   dispatched to an agent was cancelled or completed.
2. Shut down managed HTTP listeners and join registered request handlers,
   background workers and detached tasks. Child tasks register before launch.
   A recovered worker panic prevents a successful drain.
3. Atomically seal save admission after all accepted saves finish. The separate
   work sets avoid a WaitGroup Add/Wait race and do not launch waiter goroutines.
4. Under the existing save serialization, write a final gated Store snapshot,
   respecting the remaining deadline, then close the backend. Backend errors,
   load-gate failures and expired deadlines are reported as failures.

Success logs `status=drained persistence=acknowledged backup_consistent=false`
and exits zero. It means this process's tracked Store producers and saves have
finished, the final snapshot received a successful backend acknowledgement, and
backend closure succeeded. External metrics/notification delivery queues may be
discarded during cancellation; drained does not promise their delivery.

Timeout, a second signal, listener failure or persistence failure exits nonzero
and logs `status=incomplete backup_consistent=false`. A second signal cancels
the existing budget; it does not start a second drain or save. A deadline also
bounds process termination if an underlying filesystem/backend call ignores
context. Such a call may still be in flight until process exit: its persistence
outcome is uncertain, and no backend close is attempted underneath known active
producers or saves. Never resume serving after an incomplete drain.

## What this does not establish

Mongo writes remain sequential across collections and records. Partial commits
can exist after a failed write or crash; successful drain is not a transaction,
a consistent snapshot for concurrent readers, or a guarantee against outside
writers. Backend acknowledgement inherits the configured durability semantics.
JSON snapshot replacement retains its file and directory sync behavior.

There is no coordinated capture of Mongo data, application snapshot and TLS/CA
files here. A backup procedure must separately verify stopped/exclusive writers,
capture the appropriate backend and files, and restore them in isolation. A
nonzero exit or forced kill cannot establish a safe capture point. Release still
requires native operational, packaging, recovery and security acceptance.

## Verification and rollback

Regression coverage includes admitted HTTP work, queued saves and JSON reload,
private-field Mongo reconnect (hosted disposable database only), save-lock
budget, backend/load failures, repeated signals, noncooperative backend timeout,
real process signal handling, dedicated and multiplexed RPC stream cancellation,
and hijacked console peer closure. These are short tests, not a resource soak or
production backup acceptance.

Disable the opt-in flag to restore the prior signal behavior when operationally
safe. A code rollback is a reviewed revert with exact-head CI; it does not undo
persisted data. Deployment rollback still requires a verified prior image,
configuration and backend-compatible recovery plan. This change performs no
live deployment or host remediation.
