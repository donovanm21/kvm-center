# Saved-source synthetic upgrade gate

`internal/legacyfixture` pins data declarations to saved source `c018b31b74d7d75ed8c49921dc0812bf5999d81f`. `source.json` records exact file hashes. This is source provenance, not a claim that a deployed artifact was built from it.

The versioned `synthetic-saved-c018b31-bson-v1` fixture covers the 29 legacy Mongo collections plus an unknown collection. It includes declared nested entities, canonical/default BSON spellings, cross-references, jobs, expired sessions, mixed timestamp representations, all three legacy history tiers and an unknown tier, binary/decimal/numeric unknown data, and index/TTL metadata. All values are synthetic. Job configuration and TTL metadata are inert; no legacy worker or index is activated.

Acceptance has distinct outcomes:

* **Offline preservation accepted:** all original BSON/index bytes remain unchanged through conversion, verified raw-history reading, restoration from the preservation bundle and reconversion. Retained checksums are emitted as synthetic evidence. The converter continues to report `activation=false`.
* **Full legacy runtime refused:** the actual current binary receives a separate normalized copy in a fresh owned Mongo database with only `_id_` indexes. It must exit on legacy history before starting the application. Every collection/document/index and the original source must remain unchanged. Refusal is not a successful upgrade.
* **Current public runtime accepted separately:** existing history/artifact lifecycle checks cover current-format boot/read/write/restart, backup restoration and rollback to the pinned pre-history public artifact. They do not establish compatibility with the saved legacy schema or deployment.
* **Legacy rollback unproven:** no verified deployment-matched server artifact and complete restore record are supplied. Never substitute a version string, source hash, agent binary, or old application backup for that evidence.

Known unsupported semantics include alarms/affinity, legacy tag representation, tiered floating-point history, Host.AgentTLSPort, VM.HasPassthrough and guest Arch/GrowResult. Unknown fields are retained as raw BSON; typed decoding may discard them and is not accepted as active compatibility. Exact-source history stores independent five-minute/hour/day observations with sample counts and floating averages/maxima, not the public fixed-point bounded representation.

The saved legacy application's ExportSnapshot omits alarms, affinity rules, history, VM tags, command log, Discord gateway, removed hosts and pending enrollments. Its application backup is therefore insufficient for full-schema rollback. Any future rollback must restore a verified raw snapshot and required companions into a fresh destination and use a pinned original artifact; candidate writes remain separate. No expiry, archive pruning, live conversion, deployment, host action or release publishing is authorized by these tests.

## Exact deployed-source profile

`GenerateDeployed` separately pins `synthetic-deployed-6db8c723-bson-v1` to source `6db8c7237bd7cf507532391c0dc0c2e36a70f696` (v5.61.58). `deployed-source.json` records hashes for model, store, history, Mongo backend and persistence declarations. Comparing these sources with saved `c018b31` identifies the later persisted `vm_tags` addition and API-only VM.Tags (`bson:"-"`). The shared persisted fields and history declarations are unchanged. This profile generates the deployed 28 collections plus the unknown refusal collection; the saved-source profile remains 29 plus unknown.

Both profiles independently run byte-preservation, raw-history reading, restoration/reconversion and actual-binary refusal checks. The deployed profile never generates the later tag collection; no source snapshot is edited or removed to force boot, and populated legacy history and unsupported fields still require refusal. Evidence carries each profile's source/schema separately. This establishes source-shaped refusal, not successful active migration or artifact identity. Neither profile starts legacy jobs or activates TTL.

## Active host/VM component compatibility

The current Mongo writer now prepares both host and VM replacement sets before writing either. For retained identities, it preserves undeclared root fields as exact BSON values. Nested containers containing undeclared fields retain their original bytes only while their typed projection is unchanged; changing or removing such a container refuses. Duplicate/conflicting identities, ambiguous case spellings, alias conflicts and lossy BSON type conversions refuse. Known field edits remain writable; this is not a read-only adapter. Obsolete records still follow explicit snapshot deletion semantics; immutable offline originals remain the recovery authority.

This is a single-application-writer contract, consistent with the existing snapshot backend. It is not a transaction or compare-and-swap against external Mongo clients. An error during database writes can still leave a partial snapshot and requires full retry. The preflight tests specifically prove that compatibility refusal in either entity plan occurs before host/VM replacement or deletion. They do not claim cross-collection atomicity.

`TestHistoryAcceptanceEntityOpaqueLifecycle` independently constructs a host/VM component fixture with opaque root and nested values, seeds synthetic current-format authorization, boots the actual server, reads inventory, edits VM notes, waits for database persistence and restarts the server. It checks exact opaque BSON values and unchanged originals. It does not remove collections from a full legacy snapshot to force boot. Full deployed/saved fixtures retain their separate refusal gates. The component fixture contains no legacy jobs, schedules or expiring records; original fixtures and index metadata are never activated.

`legacy-field-accounting.csv` accounts for every deployed-source declared field, excluding the later saved-source tag additions. Regenerate from the repository root with `go run ./scripts/legacy-field-accounting > docs/legacy-field-accounting.csv`. The ledger distinguishes known-field write preservation, opaque retention with unsupported behavior, conditional nested retention and explicit unverified/blocked fields. Declaration parity alone is not behavioral compatibility. In particular, retaining `Host.AgentTLSPort` and `VM.HasPassthrough` does not activate legacy routing or passthrough-aware operations.

All non-host/VM collections still require complete active write and recovery accounting. Guest Arch/GrowResult, alarms/affinity and tiered history have unsupported active semantics. At the Host/VM-only gate, application JSON backups did not retain the raw Mongo overlay. The following startup-entity gate adds a scoped overlay; it is still not a full-schema lossless substitute for raw BSON backups. No full-schema activation or legacy rollback is accepted by this component gate.

## Startup entities and scoped application backup recovery

The preservation registry now also covers User, Group and Role records. All five replacement plans preflight before any entity writes or username-owner retirement. Known credential and permission changes remain authoritative; opaque values never grant behavior or appear in public model responses.

Application exports from Mongo include `entity_bson_overlay` version 1 in private `store.json`, with a matching manifest feature requirement. It carries BSON for exactly Host, VM, User, Group and Role records, reconciled against the captured typed snapshot. Capture serializes with application saves. Unknown versions/collections, malformed or oversized BSON, identity conflicts, null envelopes, duplicate/case-ambiguous fields and incompatible opaque-container edits refuse. JSON persistence transports this data and prunes deleted identities after successful saves.

Mongo recovery is explicitly limited to an entirely empty destination. Both startup and the restore method check every existing collection through a separate read-only connection before index initialization. Ordinary Mongo saves reject recovery overlays. Recovery may leave a partial destination after a database failure: discard that isolated destination and retry from the unchanged backup; never overwrite an existing or partial destination. This requires a single application writer and does not provide cross-client transactions. Online restore remains disabled.

The versioned `synthetic-startup-entity-bson-v2` component acceptance uses the actual application to read and edit inventory and a group, export its backup, restore the verified archive into a fresh Mongo destination through application bootstrap, read the restored edits, take a second backup and restart. The fixture checks opaque values and unchanged originals. This is application-semantic evidence for the five named collections, not merely an archive byte-copy test.

**This is not full-schema lossless backup acceptance.** Other collections still need active write/restore accounting, and a JSON backup can still omit their unsupported fields. Full legacy startup refusal remains mandatory. Accepted-rebuild rollback is separate and unexecuted. Its accepted baseline is the pinned filesystem-equivalent recovery archive; exact serialized config/image-ID equality remains unproven. The earlier requirement for an unavailable original artifact is superseded by that explicitly accepted rebuild target, without claiming original byte reproducibility. UI, deployment and publication holds remain.

## Passive inventory overlay version 2

Version 2 extends the scoped overlay to storage pools and VM notes. Version 1 retains its original five-collection allowlist and is accepted for recovery; reconciliation upgrades it to version 2 without inventing missing original storage/notes bytes. Unknown versions and version-1 envelopes containing new collections refuse.

Storage identities are case-sensitive `HostID|Name`; empty, mismatched, duplicate and delimiter-ambiguous identities refuse. The BSON host field is `hostid`; a `host_id` alias is explicitly refused. VM notes require canonical lowercase identities matching `_id` and `vm_uuid`; malformed records no longer disappear silently on load. Conflicting nonempty VM-embedded notes and side records refuse. Clearing notes deletes their side record; recreating it cannot resurrect stale opaque values.

The `synthetic-passive-inventory-bson-v3` component acceptance adds storage and note originals, reads storage through the actual API, changes allocation through synthetic telemetry and edits notes, then repeats application backup/fresh restore/restart. This establishes passive inventory persistence and recovery, not storage operations on a host. The full-schema refusal, single-writer limitation, immutable raw originals and all promotion holds remain unchanged.

## Retirement deny-list overlay version 3

Version 3 adds `removed_hosts` to the scoped overlay. Versions 1 and 2 retain their original collection allowlists and wire manifest requirements. Retirement `_id` and typed `HostID` spelling remain exact; in-memory lookup keys are lowercase. Case-colliding identities, mismatched fields and unsupported underscored BSON aliases refuse before replacement writes. Updates preserve the original persisted spelling. Short-name/FQDN matching remains lookup behavior, never identity merging.

The `synthetic-retirement-entity-bson-v4` fixture retains nine original documents across eight collections, including two retirement records. Actual API acceptance lists records, refuses synthetic telemetry without creating inventory, preserves one record through backup/fresh restore/restart, and un-retires the other with a database durability check, restart and backup-overlay deletion check. It never calls host-delete, enrollment, certificate, command or agent endpoints. No host decommission or certificate behavior is claimed. All original/raw full-schema fixtures and refusal gates remain intact; rollback startup fixtures and runtime execution remain separately held.


### Guest report preservation (overlay version 4)

Guest cache records use a nested `entry` document, containing `payload` and
`updatedat`, with a separate root `updated_at` used by Mongo TTL. The former
inline loader did not match that writer. The strict nested loader now checks
identities, exact types, canonical BSON spellings and equal timestamps, refusing
malformed records before index initialization. Root/map identities are lowercase;
the payload UUID may retain letter case only when its lowercase value agrees.
Whitespace, cross-format aliases and conflicting representations are refused.

`Arch` and `GrowResult` round-trip as guest-reported data. This does not implement
architecture-aware guest updates, growth dispatch or the old guest identity and
cache eviction policies. A new report replaces known fields, including clearing
an omitted grow result. Unknown root fields remain opaque; a typed edit inside a
container with opaque descendants is refused rather than dropping its values.

The separate `synthetic-guest-report-bson-v1` acceptance retains two expired
synthetic originals, proves they are not refreshed or treated as fresh, receives
one current report, reads it through the VM API, restarts, creates an application
backup, restores into a fresh database, creates a second backup and restarts again.
The second expired report retains an opaque nested entry unchanged throughout.
The disposable history CI Mongo service has its TTL monitor explicitly disabled,
and the test verifies that condition. It never dispatches a guest update or grow
operation. Original source files remain unchanged. Overlay versions 1–3 retain
their original allowlists and manifest versions while upgrading in memory to v4.

This is conditional preservation evidence, not a cache-retention redesign:
ordinary Mongo TTL still expires guest records after 300 seconds, and the current
in-memory cache may subsequently rewrite an expired record. Stale VM guest fields
also have separate lifetime behavior. A migration must keep legacy TTL/jobs
inactive and retain raw originals indefinitely; full-schema activation remains
blocked until those and remaining semantic gates are addressed.


### Unsupported namespace refusal

Populated Mongo namespaces without a current persistence representation, including
legacy `alarms` and `affinity_rules`, now refuse before index initialization.
Cached connections repeat that gate on Load, Save and application snapshot export;
no backup archive is started when export cannot account for a namespace. Empty
unknown collections are left intact but are not declared compatible. Namespace
inspection errors refuse rather than being treated as an empty database.

JSON snapshots refuse unknown top-level fields, including null/empty unsupported
feature fields, before mutating the destination. Export remains closed after a
failed initial load, preventing an empty/default backup from omitting refused data.
The current private sidecar and
supported backup formats are unchanged. Nested model-field handling is outside
this batch; all previous overlay v1–v4 acceptance remains required.

Independent `synthetic-unsupported-namespace-bson-v1` refusal fixtures cover
alarms, affinity and a future namespace alongside an expired record. Offline
copy-to-staging precedes actual-app startup refusal, with originals and indexes
unchanged. A separate running-app test first exports a readable backup, then
verifies repeated export refusal after unsupported data appears on the cached
connection, with no partial archive or changed original. Neither full-schema
fixture is stripped or altered to force boot.

This closes namespace omission paths under the single-application-writer contract.
Inspection is not atomic against an external concurrent writer, and an allowlisted
collection does not establish compatibility of all its fields, indexes or behavior.
Alarms/affinity implementation and complete legacy activation remain unmet.

## Audit events: bounded active component (overlay v5)

The audit component supports canonical legacy `audit_events` records through
read/filter, application-generated append, chronological retention, restart,
application backup, fresh restore, second backup and restart. The native
acceptance fixture contains all ten deployed declared fields plus an opaque
Decimal128 value. Unknown action strings are displayable historical data;
interpreting or replaying their original operations is not supported.

Admission requires nonempty matching `_id`/`id`, nonzero BSON dates, exact declared
field types, and at most 10,000 events. Ambiguous aliases, duplicate identities,
and oversized input refuse before indexes or writes; no newest-only query or
silent deduplication approves partial input. The exact historical `actorid` alias is explicitly canonicalized to `actor_id`
through the existing strict decoder; conflicting, mistyped or mixed-case aliases
refuse. Offline originals remain immutable.

Ordering is explicit: ascending BSON millisecond time, then ID, with newest-first
reads. Appends use the same ordering before trimming the oldest events to 10,000;
JSON and Mongo restart therefore agree for tied or out-of-order timestamps.
Retained events preserve opaque BSON. Active-ring eviction is deliberate and
never deletes retained offline originals. Audit TTL remains disabled for legacy
preservation acceptance; this is not approval to activate the archived TTL index.
Overlay v5 reserves 10,000 audit records independently of the existing 10,000
other-record allowance, within the unchanged 16 MiB total raw-data bound. Original
v1–v4 backup manifests remain readable and are tested without rewriting them.

### Essential acceptance before this component is complete

- Strict, bounded BSON/JSON admission; byte-preserved originals and opaque values.
- Deterministic ordering and cap-boundary retention, including a full-ring backup
  with other inventory; no resurrection after intentional eviction.
- Actual application read/filter and durable generated append across restart.
- Backup/fresh restore/second backup/restart and original v1–v4 archive support.
- Independent exact-head review, passing exact-head CI and post-merge verification.

### Remaining activation and release blockers

Full-schema activation still requires per-collection field/reference/operation
semantics and full-fixture recovery. Alarms, affinity, legacy history/tag routing,
guest expiry/projections and routing/passthrough remain unresolved. The intentional
unknown namespace in the full fixture must receive an explicit supported meaning
or remain inactive; it cannot be deleted merely to obtain a passing boot. Legacy
jobs and TTL stay inactive. The archived writer loses five known opaque values,
so rollback remains separate original-snapshot recovery with explicit accounting
for later writes, not a lossless ongoing writer. Native quiescence, authorized UI
acceptance and the separate promotion gates remain release blockers.

Audit shipping/Discord delivery, new filtering features, indefinite active audit
retention and replay of historical actions are optional or separate components;
none is required to establish this bounded local audit reader/append/recovery path.
