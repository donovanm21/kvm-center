# Canonical BSON compatibility and preservation boundary

This change is a code and synthetic-test prerequisite for offline compatibility work. It does not migrate a database, activate a candidate, change retention policies, or authorize deployment. Original raw BSON snapshots and all legacy history must remain unchanged and retained without expiry or pruning.

| Model | Canonical output/index field | Accepted old public alias | Exact BSON type |
| --- | --- | --- | --- |
| VM | `host_id` | `hostid` | string |
| Session | `expires_at` | `expiresat` | datetime (milliseconds) |
| MigrationRecord | `started_at` | `startedat` | int64 (Unix seconds) |
| AuditEvent | `actor_id` | `actorid` | string |

A single exact spelling or two equal, identically typed aliases decode successfully. Conflicting aliases, null, coercible-but-different scalar types, duplicate top-level keys, case variants of either spelling, and malformed BSON fail before changing the receiver. Missing fields retain historical zero-value behavior; explicit empty strings and zero timestamps are not invented or discarded. JSON wire tags are unchanged. New BSON writes use only canonical keys; the existing Mongo indexes already use those keys. No indexes are rebuilt on existing databases by this change.

The decoder never edits input bytes. During alias normalization it retains unrelated raw element values in its temporary document, but the typed model still ignores unknown fields. Re-encoding that model is **not lossless full-schema preservation**. The raw original snapshot, including unknown fields and separate collections, remains necessary. Mixed legacy/canonical databases are not supported activation targets: server-side indexes and sort order do not reinterpret old aliases. Canonical normalization belongs only in a separately reviewed offline staging process.

Synthetic integration tests keep original BSON only in memory and write to a fresh random loopback Mongo database. They verify canonical values, keys, indexes, wrapper `_id` fields, reconnect/load behavior and unchanged originals. Dates are future synthetic dates; active migration fixtures have no retention expiry. The packaging probe still refuses legacy `perf_history` and reports full v5.61.58 compatibility as false even when these four shared fields survive.

## Conservative staging and history requirements

Do not point `MongoBackend.Load`, the application, a telemetry process, a background writer, or TTL/index initialization at original data. `Load` can create indexes before refusing legacy history, and normal runtime behavior may expire sessions, apply migration retention, or prune bounded history. Aligning session writes with `expires_at` also makes those writes eligible for the existing TTL index. These effects require a separate activation decision; passing codec tests does not authorize them.

A future preservation workflow must take immutable backend-native/raw BSON snapshots with hashes and provenance, including every collection and original history record. Work only on copied inputs in an isolated destination with no application writers and no TTL expiration. Preserve originals indefinitely; do not substitute archive-and-discard, expiry, pruning, or a normal-runtime hydration check for preservation. Rollback must restore an original snapshot into a fresh isolated database with the original pinned binary; it must not run the old binary over rewritten candidate data.

Lossless archival preservation is possible by retaining original raw bytes. A read-only legacy-history view is also feasible as a separate reader over those preserved copies, but is not implemented here. Such a reader must expose original sample timestamps, values, units and schema provenance without passing records through the bounded active history store. Unknown fields or unknown units must stay available as raw data or be explicitly marked unsupported. It must not synthesize sample counts, maxima, fixed-point scaling or aggregate meaning from insufficient evidence. A view cannot claim active-format equivalence without a separately validated mapping.

Full-schema conversion, a read-only legacy-history reader, archive browsing/export, deployed lineage verification and isolated backup/restore acceptance remain outstanding. No converter, history conversion, archive expiry, live migration, retention pruning or post-activation write test is included in this change.
