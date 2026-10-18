# Bounded legacy affinity placement gate

This component accepts only explicitly converted synthetic rules from deployed
source `6db8c7237bd7cf507532391c0dc0c2e36a70f696`. It supports `together` and
`separate`, `must` and `should`, enabled/disabled rules, and unique canonical UUID
members. Name selectors, tag selectors, unknown fields, aliases, types, indexes,
and contradictory required rules are refused. This is not universal legacy
compatibility. Saved-source tag assignment compatibility does not establish tag
selector compatibility.

`kvm-offline-staging -mode legacy-affinity-state -bundle <verified-bundle>` emits
version 1 `affinity_state`. This immutable rule state includes exact original
BSON bytes; the original offline bundle, indexes and provenance must also remain
retained indefinitely. The ordinary `affinity_rules` namespace is still refused.
The complete legacy fixtures are unchanged and remain outside active support.

Direct live/offline migration and maintenance drain share admission before grant
issuance and dispatch. A consistent inventory must identify every enabled rule
member, the requested UUID/name/source, and a unique enrolled destination. Stale
fleet inventory, duplicate identities, UUID/name crossmatches and conflicting
request fields refuse. Offline hosts are included; they are not treated as empty
capacity. Projected placement must satisfy every enabled required rule, including
transitive together constraints. This deliberately refuses incremental repairs
that would leave another required rule violated. Preferences produce warnings;
direct requests expose `X-Affinity-Warnings` and drain records an audit warning.
Drain filters candidates and repeats admission before dispatch. Unplaceable
members receive an explicit audit reason. VM creation is refused while this
bounded policy is active because creation admission is not implemented.

A new placement upgrades unreserved version 1 state to a version 2 ledger and
first saves a `prepared` reservation. Only the owning in-process handler receives
an abort lease. If its final pre-grant check refuses, it can durably replace the
prepared reservation with an immutable aborted request record. A different
request ID can then undergo fresh admission; the aborted ID cannot replay.
Aborted records are never pruned. At 256 records, admission refuses rather than
losing replay protection. Original raw rule BSON remains unchanged.

Before any migration grant is exposed or command dispatched, the handler saves
`dispatch_possible`. After crossing this boundary, HTTP success/refusal/failure,
timeout, cancellation, migration history and telemetry never release the
reservation. A failed prepare or dispatch-state write keeps the barrier; an
uncertain abort write also keeps the running process blocked. If that abort
actually committed, restart loads its aborted identity and can admit a new ID.
Recovery never manufactures a process-local abort lease: recovered `prepared`
reservations remain blocked. Existing version 1 reservations have no dispatch
phase and remain unresolved. No clearing or reconciliation endpoint is provided.

This is repeated admission after a proven pre-dispatch abort, not repeated native
migration or successful host drain. There is no release after possible dispatch.
JSON snapshots and Mongo fresh-destination restore retain originals, phase and
aborted identities; backup feature schema records `affinity_state: 2` for upgraded
state. Version 1 backups remain readable without inventing execution evidence.
Mongo saves reject ledger downgrade, removed/changed aborted records, altered
originals and any release of a dispatch-possible reservation. Concurrent external
database writers remain unsupported. Do not use an earlier backup, edit/delete
the singleton or reimport rules to bypass uncertainty. Historical backups do not
establish the outcome of later moves; post-upgrade-write rollback is unproven.

## Native acceptance dependency

Releasing a potentially dispatched reservation requires a separate, versioned,
request-correlated native evidence protocol. It must bind the exact UUID, source,
destination, immutable command identity and execution generation; establish that
all relevant workers, libvirt jobs and partial offline transfer/finalization paths
have stopped; and exclude competing writers through observation and subsequent
admission. Under that barrier, complete fresh inventories must prove source
absence and destination presence for success, or unchanged placement after a
verified abort. A source query error is not absence. A generic `active=false`,
worker return, exit status, cancellation request or recent telemetry timestamp is
not such proof. PR82 command closure remains narrower than native quiescence.

Native acceptance must cover live/offline success, pre-effect refusal, partial
failure/copy, timeout/lost response, cancellation, agent/server restart, stale or
reordered observations, replay and competing placement. Synthetic injected
results can test the protocol's state machine, but cannot certify that its native
producer excludes residual jobs or other writers. No existing protocol supplies
that proof, so dispatched reservations remain blocked.

Acceptance uses synthetic inventory and intercepted HTTP dispatch only. It does
not prove actual native migration, source absence, destination presence or native
quiescence. UI execution, public publishing and deployment remain on hold.
