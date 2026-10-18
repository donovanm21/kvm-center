# Offline preservation staging (synthetic contract)

`cmd/kvm-offline-staging` converts a **synthetic offline snapshot** to a new,
private preservation bundle. It has no database URI, credentials, server startup,
activation, resume, deletion or retention option. It never instantiates indexes,
telemetry, jobs or TTL. Every successful manifest says `activation: false`.

This is full-collection **raw preservation**, not full-schema application
compatibility. The server still cannot round-trip arbitrary legacy fields or
hydrate all legacy history. Issue #65 remains open. No real-data conversion or
upgrade is authorized or supported by the accepted source schema.

## Input contract

A flat directory contains `snapshot.json` and, for every declared collection,
`<name>.bson` and `<name>.indexes.bson`. BSON files contain concatenated, exactly
framed BSON documents; empty collections/index lists use zero-byte files. Index
metadata is raw BSON too, not Extended JSON. No undeclared files are accepted.
Collection names match `[a-z][a-z0-9_]{0,63}` and declarations are strictly sorted.
The JSON manifest has exactly these fields:

```json
{
  "contract": "kvm-offline-bson-v1",
  "source_schema": "synthetic-full-bson-v1",
  "source_build": "synthetic-fixture-v1",
  "target_artifact_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "verification_clock": "2026-01-01T00:00:00Z",
  "consistency_evidence": "synthetic generator and fixture version",
  "companion_evidence": "synthetic fixture; no real companion material",
  "collections": [
    {"name": "hosts", "documents": 0,
     "data_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
     "index_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}
  ]
}
```

The sample target hash is deliberately synthetic. Hashes bind bytes; they do not
prove source lineage, snapshot consistency, companion completeness or producer
identity. These claims remain blockers in every output. Build and record the
converter's exact source commit and executable SHA-256 externally before use;
the versioned contract identifies its normalization rules, not its executable.

Limits: 128 collections, 128 MiB total source bytes, 16 MiB per BSON document,
100,000 documents per collection, depth 32 and 4 MiB JSON/ledger. Oversized input
is refused, never truncated. Input/output must have no symlink ancestor, file
symlink or hard-linked file. Filesystems/platforms without verifiable link counts
or directory fsync support fail closed. Use an exclusively owned local directory;
this is not a synchronization protocol for a concurrent writer or hostile owner.

## Preservation and refusals

All raw originals, indexes, collection/document order, `_id` values, unknown
fields and raw history are retained indefinitely. The normalized copy changes
only four top-level aliases: `vms.hostid`, `sessions.expiresat`,
`migrations.startedat`, `audit_events.actorid` become their snake_case keys.
Equal typed dual aliases collapse in the normalized copy only. No timestamps,
expiry dates, identities, units, aggregates or defaults are invented. Required
mapped fields must exist; audit actor may be absent or empty. Current VM host
references must resolve to an exact host `_id`; historical actor references may
remain unresolved. IDs are nonempty strings or ObjectIDs; other forms are refused.

Every duplicate BSON key (including nested keys), malformed frame, noncanonical
array, alias spelling/type/conflict and unsupported BSON type is refused. Supported
raw value types are document, array, string, binary, ObjectID, bool, date, null,
int32, int64, double, timestamp, decimal128, minKey and maxKey. JavaScript, regex,
DBPointer and deprecated types require a separate explicit contract.

Only single-field ascending/descending indexes with binary string/ObjectID
uniqueness are supported. Partial/collation/compound/multikey/hashed/text indexes,
unknown options and ambiguous unique null/missing/numeric values are refused.
Sparse unique missing values are allowed. Target VM UUID/user-name uniqueness
is checked even when source metadata omits the index. Existing known application
indexes must match their required direction/options/TTL; nothing creates a missing
index. TTL fields present in records must be exact BSON dates and are retained,
including already-expired values. Metadata is preserved, never activated.

## Commands and durable state

```sh
go build -o /tmp/kvm-offline-staging ./cmd/kvm-offline-staging
/tmp/kvm-offline-staging -mode convert -source /tmp/synthetic-input -bundle /tmp/new-stage
/tmp/kvm-offline-staging -mode verify -bundle /tmp/new-stage
/tmp/kvm-offline-staging -mode history -bundle /tmp/new-stage
```

`history` prints collection, ordinal and SHA-256 only. Explicit `-raw-history`
streams original concatenated BSON to stdout; protect that output as source data.
It does not interpret timestamps, units, sample counts or aggregates. Both
`perf_history` and `host_history_state` are retained/read even when mixed;
**mixed history is not an activation candidate**.

Output creation is exclusive (directory 0700, files 0600). Each original, index,
normalized file and per-document ledger is fsynced and read back. Source bytes
are rechecked. Only after this succeeds is the pending manifest fsynced and
atomically renamed to `manifest.json`, followed by directory fsync. An interrupted
write leaves NOT_READY. The converter never cleans or resumes partial output;
use a fresh destination after investigation. A committed but not acknowledged
rename is resolved by full verification, not blindly retried writes. Existing
verified matching output is a verify-only no-op; changed/partial output refuses.

`verify` rechecks the complete inventory, original hashes, framing, identities,
links, index constraints, transformations, ledger and normalized bytes. The
history reader returns bytes from that same verified in-memory snapshot. No
readiness claim is based solely on a status label or hash.

## Recovery evidence and remaining gates

Synthetic tests span every current collection plus representative alarms,
affinity, extended/nested/binary fields, unknown collections and legacy history.
They demonstrate deterministic reopen, preservation-bundle restoration to a
fresh offline snapshot, reconversion and byte-identical rollback originals.
They inject failure at file-create/write/fsync and commit boundaries and reject
partial/tampered output. This establishes offline file recovery only, **not**
application restart or full database rollback compatibility. Existing isolated
Mongo/runtime tests cover the narrower four-field public schema contract.

Before any future real-data transition: establish the exact source schema and
semantics, independently prove a coherent backend-native snapshot and encrypted
companion backup, prove runtime preservation on separate synthetic copies, pin
both executable identities, and obtain explicit approval for a bounded write-free
transition and fresh-database rollback. Never start an old binary on converted
new writes. Originals and history must never be expired, pruned or discarded.

## Pinned legacy aggregate reader (synthetic only)

`kvm-offline-staging -mode legacy-history -bundle /tmp/new-stage` reads the
`perf_history` writer contract at source `6db8c7237bd7cf507532391c0dc0c2e36a70f696`
(v5.61.58). The verified snapshot must declare that exact source build. That
manifest declaration selects an interpretation; it is not deployed executable
provenance. The versioned output is `legacy-history-6db8c723-reader-v1` and
includes each original record's SHA-256. Explicit use prints host identities
and historical values; protect the output as source data.

The three tiers are 5 minutes / 576 points, 1 hour / 720 points and 1 day / 365
points. Timestamps are UTC bucket starts in Unix seconds. CPU, memory and storage
averages and peaks are stored percentages. Each tier was folded independently
from raw samples. `n` is its raw sample count; `vms` is the last raw sample's VM
count. Stored floating-point values are returned unchanged, including rounding
that can put an average slightly above its peak. The reader does not compute
rollups or infer missing samples, coverage, downtime or cross-tier consistency.
Retention is by point count, not elapsed wall time: old valid points are read
without expiry, trimming or deletion.

This is a bounded reader: it accepts exact fields and BSON types, integer counts,
finite percentages in [0,100], positive sample counts, nonnegative VM counts,
strictly increasing aligned timestamps from 2000 onward and the pinned tier
size limits. These limits are admission restrictions, not a claim that the old
writer enforced every one. Unknown fields/tiers, aliases, malformed arrays,
null tier arrays, mixed current history, other source contracts and out-of-bound
values refuse before any semantic output. Raw `history` reading and the original
bytes remain available after semantic refusal. Empty or absent tiers represent
no stored points; no missing tier is synthesized.

The separate `synthetic-legacy-history-reader-v1` fixture exercises averages,
peaks and sample counts across all tiers, process reopen and fresh offline
restore/reconversion. The full deployed fixture remains intact and refuses its
unknown semantics. This does not admit legacy history into the application's
current history store, prove app history read/write/restart, or establish old
writer rollback. Existing archive versions and full-schema refusal gates remain.

## Bounded application history archive

`-mode legacy-history-state` exports a version-1 `legacy_history_state` value
from the same verified bundle. Place this value under that key in a separate
staging snapshot. It retains each exact original BSON record; it does not replace
the immutable source bundle. Admission is limited to 64 host records and 2 MiB
of raw BSON, with identities addressable by the existing history API. Oversized
or unsupported inputs refuse without trimming. Preserve rejected originals too.

Fresh Mongo recovery uses the existing entirely-empty destination guard, including
before startup index initialization. It writes the original `perf_history`
records and a `legacy_history_contract` marker binding the complete record set,
source contract and schema version. Only the ordinary identity index is allowed
on these collections. Normal writes never modify or delete these records. Changed,
missing or additional records, mismatched markers and other indexes refuse load,
save and backup, including on a cached connection. Concurrent external writers
are unsupported. A partially failed recovery requires a new empty destination.

Authenticated `GET /api/hosts/<id>/history?source=legacy` requires `host.view` and
returns the original stored tiers, hash, units and aggregate meanings, explicitly
marked read-only. Historical hosts need not remain in current inventory. Range
and tier filtering are refused; points are never aged out. Default/current reads
and new telemetry use the independent current history store. No missing samples,
fixed-point sums or exact last-sample timestamps are inferred, and old aggregation
jobs are never resumed.

Application snapshots carry both stores; backup manifests declare
`legacy_history_state: 1`. Existing overlay archive versions 1–8 remain supported.
The native acceptance gate checks explicit offline export, app boot/read/current
write/restart, fresh backup restore and a second backup/restart against unchanged
raw history. This closes only the bounded immutable-history application component;
full legacy fixture activation, unknown semantics, continuation of old aggregation
and post-upgrade-write rollback remain unproven. UI, native quiescence, deployment
and release authorization are separate gates.

## Saved-source tag assignment conversion

`-mode legacy-tag-state` exports the existing version-1 `vm_tag_state` value
from a verified bundle declaring saved source
`c018b31b74d7d75ed8c49921dc0812bf5999d81f`. Tags were introduced after deployed
source `6db8c723`; that deployed profile has no `vm_tags` and is not accepted
as tag-conversion provenance. The source declaration selects a contract, not
proof of a deployed executable.

Place the exported value under `vm_tag_state` in a separate fresh staging
snapshot. This conversion accepts only exact `{_id,tags}` BSON records with
canonical lowercase UUIDs and sorted, unique, nonempty arrays of 1–24 canonical
ASCII tags. It does not normalize ambiguous originals. Unknown fields, aliases,
types, duplicate identities, unsupported index semantics and mixed current tag
state refuse. Only the ordinary `_id` index is admitted. The current limits of
4,096 assigned identities and 1 MiB encoded state apply by refusal, never trimming.
The ordinary application's refusal of populated legacy `vm_tags` is unchanged.

Retain the original offline bundle, raw BSON, indexes and provenance indefinitely,
separately from application backups. **Application backups contain editable tag
assignments, not original BSON/indexes or conversion provenance.** Recovery must
retain both artifacts. Restoring a current application backup restores its latest
assignments, including intentional clearing; it must not reapply the original
export and resurrect old tags. Raw-original restoration/reconversion remains a
separate, explicit path and is not rollback of post-conversion edits.

The separate `synthetic-legacy-tags-c018b31-v1` component fixture includes a live
assignment and an orphan. Native JSON and Mongo acceptance use the built offline
converter and application to prove reads, inventory filtering, conditional edits,
new assignments, stale-edit refusal, clearing, restart, backup/fresh restore and
a second backup/restart. Orphans stay stored and backed up even though per-VM tag
GET/PUT requires inventory. Original BSON/index hashes remain unchanged through
active edits and raw restore/reconversion. Full-schema fixtures remain intact;
existing archive versions 1–8 remain supported.

This proves bounded assignment compatibility. Saved-source `tag:<name>` alarm
and affinity selectors are not enabled or verified. Saved-source missing-VM
pruning is not resumed: current tags deliberately survive inventory omission.
It does not prove full saved-source behavior, full-schema activation, UI/native
operations or lossless post-upgrade-write rollback. Existing holds remain.

### Bounded affinity rules and placement reservations

The separate `synthetic-legacy-affinity-6db8c723-v1` fixture and
`legacy-affinity-state` export preserve original rule BSON while supporting only
canonical UUID-based `must`/`should` together/separate rules. The exported
`affinity_state` is an explicit current snapshot namespace, not permission to
load ordinary legacy `affinity_rules`. Preserve original BSON, indexes and
provenance separately and indefinitely. Full-schema fixtures are not filtered.

The application shares a durable, conservative reservation gate across direct
migration and drain; see [the affinity contract](../server/docs/affinity.md).
Version 2 distinguishes prepared and dispatch-possible reservations. Only the
owning handler can durably abort a preparation before grant exposure, retaining
its request identity indefinitely. Backup/fresh restore preserves the ledger;
recovered reservations never gain an abort owner. Names, tag selectors, rule
edits, creation admission, post-dispatch reservation reconciliation and
native migration success remain unsupported or unproven. Existing archive
versions 1–8 and the pinned rollback package retain their previous contracts.
