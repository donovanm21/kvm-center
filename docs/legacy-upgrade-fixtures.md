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
