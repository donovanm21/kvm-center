# VM tag persistence foundation

This batch adds Store-owned tag data and its persistence contract. It does not
expose a tag API, modify VM inventory models, add UI, evaluate alarms or affinity,
or influence placement. Tags never cause agent or host operations.

## Format and ownership

JSON snapshots use an optional `vm_tag_state` field:

```json
{"version":1,"by_vm":{"11111111-1111-4111-8111-111111111111":["database","production"]}}
```

Mongo uses a `vm_tag_state` collection with one document containing
`_id: "singleton"` and the same envelope under `state`. A missing field or empty
collection means no assignments. An explicit version-1 empty `by_vm` clears
assignments; Store snapshots omit the envelope when empty. Explicit null,
unsupported versions, unknown or duplicate envelope fields, noncanonical field
spellings, malformed identities, and noncanonical persisted tags fail loading.
Mongo also rejects unexpected singleton identities, outer fields and additional
documents. These checks are specific to tag state; unrelated legacy snapshot
fields keep their existing compatibility behavior.

`SetVMTags` stages an in-memory edit under the Store lock and returns validation
errors before changing state. It normalizes UUID case and whitespace, lowercases
and trims tags, removes blanks and duplicates, then sorts tags. Persisted UUIDs
use lowercase hexadecimal 8-4-4-4-12 syntax. Tags contain 1–32 ASCII letters or
digits, with `.`, `_` and `-` allowed internally. Limits are 24 input entries per
edit, 4,096 tagged identities, and 1 MiB of compact encoded state. Input JSON may
use up to 4 MiB for formatting; BSON input is capped at 2 MiB. Canonical compact
state must still fit the 1 MiB limit. The input-entry limit applies before
deduplication. Getters and setters do not retain caller-owned slices.

An empty edit explicitly removes an assignment. Missing or incomplete inventory
never prunes tags. UUID identity survives renames and moves without tying tag
ownership to a host inventory list. Each Store owns its map and dirty hashes.

## Persistence and backup behavior

The setter does not save or promise durability. A future API must call
`SaveChecked` before a durable acknowledgement and define concurrent-edit and
retry behavior. Snapshots capture tags under the existing Store lock, preserve
the private wire sidecar, and register a separate dirty hash. Successful saves
commit hashes; failed or partial writes retain the existing full-retry behavior.
JSON reload, Mongo reconnect, explicit clearing and backend migration compare
tag content, not only record counts.

Mongo validates existing tag namespaces before writing any collection. This is
a compatibility preflight, not a transaction or protection against concurrent
external writers. Tags are saved after other collections. A crash can leave a
partial multi-collection snapshot; exclusive writer ownership and recovery are
still operational requirements.

Manual and scheduled archives contain tags in `store.json` and record
`feature_schemas: {"vm_tag_state": 1}` in their manifest when present. Metadata
comes from the exact captured snapshot bytes. The outer format remains
`persistData.v2`; feature metadata does not force older software to understand
it. Archive generation does not consume dirty state or establish a consistent
capture of separately changing TLS/CA files and backend data. Online restore
remains disabled.

## Compatibility, rollout and rollback

The independent unversioned `vm_tags` format is not implicitly converted. JSON
containing that field, including null or empty values, is rejected; Mongo rejects
a populated `vm_tags` collection. A separately reviewed offline converter must
establish semantics and preserve the source before any import. No real deployment
data is migrated by this batch.

Version guards protect readers that implement this contract. They cannot make an
older binary safe: an older JSON codec drops unknown tag state on rewrite; an
older Mongo writer may retain the collection but omit it from memory and backup.
Tests explicitly demonstrate these limits. Do not downgrade over newly written
state or use an old backup/export path to preserve tags.

Before rollout, stop or verifiably quiesce every writer, capture a backend-native
backup plus required application material, verify isolated recovery, and retain
the prior executable or immutable image and configuration. Rollback requires
stopped writers and the compatible pre-upgrade backup; newer tag changes will
not exist in that backup. Source tests do not establish deployed recovery or
release readiness.

## Follow-up prerequisites

- Tag API: existing permissions and scoped-token checks, checked durable
  acknowledgement, concurrent edits, retry/conflict semantics, and copied VM
  response overlays. UI follows the API contract.
- Alarms: independently versioned definitions with complete snapshot, Mongo,
  clear and backup coverage; separate transient firing state and synthetic
  evaluation before real notification delivery.
- Affinity: independently persisted definitions and advisory evaluation before
  placement or maintenance enforcement.
- Tag selectors: require both tag API semantics and the corresponding alarm or
  affinity foundation; empty selectors and missing inventory need defined tests.
- Performance history: Store-owned bounded retention and rollups, lock ordering,
  stale-host policy, and per-host persistence round trips before visualization.

Validation uses synthetic state, disposable hosted Mongo databases, JSON and
archive round trips, malformed/unknown-version fixtures, and race-tested Store
operations. It does not require native hypervisor access.
