# Bounded UI power fixture watchdog

`cmd/kvm-ui-fixture-watchdog` is a separate opt-in fixture observer for one
reviewed diskless, networkless, 1-vCPU/256-MiB BIOS guest. It never calls Start,
serves a listener, creates credentials or configures the production agent. Only
the existing UI/production-agent path may issue the two approved power actions.
It reuses the exact original/observed PR63 inactive-XML ownership profile.

A private runtime manifest contains `Version:1`, exact local `Hostname`,
`AgentSHA256`, and `Config:{Identity:{UUID,Name,Owner},StateDir}`. Invocation
requires its SHA-256, the helper's SHA-256 and `-allow-disposable-libvirt`.
The production agent path, virsh binary and Unix libvirt socket are fixed. The
lab independently verifies services, deployed command semantics, empty targets,
artifact hashes and unchanged shared resources before launch. No credentials or
production configuration belong in the manifest.

The empty state directory must be 0700 with no symlink ancestors. Files are
0600 and hard links/symlinks are rejected. A nonblocking exclusive flock remains
held for the entire helper lifetime. The lock file is retained as evidence;
recovery reacquires its lock instead of deleting it or guessing from a PID.

State flow: durable ownership journal -> define -> strict normalized fingerprint
pin -> PREPARED_STOPPED -> durable ARMED -> RUNNING_OBSERVED -> STOPPED_OBSERVED ->
CLEAN. The setup expires after120s. Arming starts a fixed monotonic45s deadline
**before** saving/acknowledging ARMED. Duplicate signals cannot rearm. `arm` and
`finish` markers are fully written/fsynced before atomic publication. Never click
Start until the committed ARMED acknowledgment is verified. The helper polls at
250ms intervals; native calls have their own3s timeout/1s process wait bound and
share the remaining phase deadline, so250ms is not a latency guarantee.

Normal observed running->shutoff cleanup never destroys. Deadline, cancellation,
operator finish or recovery may destroy only a revalidated exact owned domain in
`running` state, then verify shutoff, undefine and verify both UUID/name absent,
within15s cleanup. Such intervention always fails UI acceptance. Paused/unknown
state, ownership/XML drift, corrupt/changed journal or unexpected files refuse
mutation and retain evidence/residuals. Observation guard failures are latched;
a later convenient read cannot erase the refusal. No fingerprint reset, second
guest or broad cleanup is available.

`-mode recover` never resumes the UI sequence. It requires the same manifest and
committed journal, released old-helper lock, exact ownership and allowed state.
An interrupted define is pinned durably before destructive cleanup. Missing or
corrupt journals refuse; `Clean` cannot authorize a reappearing domain. SIGINT/
SIGTERM trigger the bounded cleanup path; no hard-kill injection is part of this
workflow. Archive evidence before separately removing exact fixture files.

**Native observation is not UI attribution.** Match both production task/request
IDs and terminal outcomes before claiming UI Start/Force off passed. A delayed
queued request can arrive after the watchdog deadline; the lab must account for
terminal operations and independently recheck final absence/shared baseline.
The helper cannot revoke production-agent requests. No late command or cleanup
failure permits extending45s or widening cleanup. Inactive XML does not detect
every live-only change; no hotplug or other configuration writer is a strict
prerequisite. Errors, kernel stalls or concurrent mutation can prevent cleanup;
report residuals instead of bypassing the guard.

Private identity/host details and executable commands belong in the locally
reviewed lab handoff. Public reports contain sanitized scope, commit/artifact
hashes, state/outcome and cleanup evidence only. This is UI power acceptance,
not UI create/delete, guest OS boot/data/network, production enrollment, cross-host
operations or release readiness.
