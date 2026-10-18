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

An accepted placement first saves a durable reservation. One unresolved
reservation blocks all later placement operations, including replay of the same
request. Persistence errors keep the in-memory barrier and prevent dispatch.
An HTTP result, timeout, cancellation, migration history entry or telemetry update
never clears it. There is intentionally **no reservation reconciliation or rule
editing API yet**. This batch proves one admitted operation and subsequent safe
refusal, not repeated production migration or successful host drain. Do not use
an older backup, edit the state, delete the singleton or reimport the original
rules to bypass an unresolved reservation. Safe reconciliation needs a separate
reviewed evidence contract before broader activation.

JSON snapshots and Mongo fresh-destination restoration retain the originals and
reservation. Backup feature schema `affinity_state: 1` records the capability.
Restoring a backup with a reservation remains blocked and does not replay the
command. Mongo ordinary saves refuse original changes, reservation clearing and
missing previously reserved state. Concurrent external database writers are not
supported. Historical backups taken before a reservation do not prove that a
later placement never happened; post-upgrade-write rollback remains unproven.

Acceptance uses synthetic inventory and intercepted HTTP dispatch only. It does
not prove actual native migration, source absence, destination presence or native
quiescence. UI execution, public publishing and deployment remain on hold.
