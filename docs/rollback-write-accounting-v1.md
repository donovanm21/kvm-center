# Synthetic post-upgrade write accounting v1

`synthetic-current-write-accounting-v1` is an offline report of differences
between two verified current-format application backups. Both inputs bind the
same current source revision and built-artifact hash, plus the separately
retained legacy source-file hashes. It does not compare an old writer's output,
replay edits, convert current state back to a legacy schema, or approve rollback.
`activation`, `replay_supported` and `rollback_safe` are always false.

This closes a bounded accounting gap left by supported composition v1: restoring
an original snapshot excludes later writes, so those writes must be identified
explicitly instead of silently described as recovered. Original-to-current
conversion accounting remains a separate gap. The archived writer's previously
verified opaque-data losses are unchanged.

## Input and report contract

The internal `rollbackaccounting.Compare` function has no database connection,
filesystem write, command execution or activation path. Its caller supplies the
verified snapshot data. The hosted composition acceptance obtains these inputs
from application-produced archives after archive checksum/feature validation.
It binds every application JSON section, the complete thirteen-collection entity
BSON overlay and all seventeen runtime index inventories. Overlay coverage is
not claimed to be full-database coverage: other application sections, including
history, tag/affinity states, operator tasks and private storage fields, are
accounted through their separate exact JSON-section hashes.

The report inventories present and absent collections separately from empty
collections. Every before/after document reconciles into unchanged, added,
deleted or changed counts. It includes record hashes even when unchanged,
BSON field type/value hashes for changes, section inventories/deltas and index
inventories/deltas. Nested documents and arrays are compared as whole values;
array identity is never inferred. Raw BSON ordering changes remain raw changes.
No normalization is silently treated as equality.

Duplicate identities or keys, invalid BSON framing, noncanonical array keys,
duplicate JSON keys and mismatched source/artifact/original bindings refuse.
Identifiers and field/section keys are hashed; raw values are not emitted.
Collection names are bounded synthetic names. Inputs and the canonical JSON
report have SHA256 bindings; document ordering does not change the report.
The report is evidence, not a substitute for retained original or current data.

## Hosted qualification

The existing supported-composition test takes a baseline backup immediately
before its four documented API edits and compares it with the post-edit,
post-restart backup. It checks:

- VM note, group description, disabled alarm name/timestamp and VM tags.
- The authorized synthetic token's last-use timestamp without altering its other fields.
- Exactly four generated audit records: baseline backup export and the note,
  group and tag edits; all previous audit records remain unchanged.
- Exactly one completed server-side group-update task, with no native dispatch.
- Every other typed snapshot section and raw entity field unchanged, and no
  runtime index changes.

The report is emitted only when every difference matches this explicit plan.
The same run still performs fresh-database restore, second backup and restart.
All prior composition and adversarial refusal gates remain required. No new
credentials, external service, native execution, security configuration, UI,
release or deployment is introduced.
