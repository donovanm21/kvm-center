# Supported-component composition v1

`synthetic-supported-composition-v1` is an independently authored synthetic
coexistence fixture. It is not the deployed full-schema fixture with unsupported
collections removed, nor evidence that arbitrary legacy snapshots can activate.
The unchanged deployed/saved adversarial fixtures remain required refusal gates.

The fixture composes these independently pinned inputs:

| Input | Source contract | Admission and tested boundary |
| --- | --- | --- |
| Thirteen entity collections | deployed `6db8c7237bd7cf507532391c0dc0c2e36a70f696` declarations | host, VM, storage, note, user, group, role, retirement, expired guest report, audit, revoked API token, session, disabled CPU alarm; private overlay v8 |
| Three retained history tiers | `synthetic-legacy-history-reader-v1`, deployed source | explicit converter output; raw originals and independent aggregate values remain read-only |
| Two UUID affinity rules | `synthetic-legacy-affinity-6db8c723-v1`, deployed source | explicit versioned state conversion and recovery; no placement dispatch |
| Two VM tag records | `synthetic-legacy-tags-c018b31-v1`, saved `c018b31b74d7d75ed8c49921dc0812bf5999d81f` source | explicit versioned state conversion; tag read/edit and recovery |

Mixed source contracts are intentional and retained separately; their union is
not presented as one historical deployment. Two offline hosts and three stopped
VMs satisfy all affinity references, with the required separate pair on different
hosts. Users, roles, group, session, tokens, alarm, notes and storage reference the
same inventory. The retirement identity is separate. There are no agent URLs,
notification sinks, executable schedules, pending enrollments or native jobs.
A separate ephemeral test token authorizes the application requests; it is not a
legacy credential and is not included in retained public evidence.

## Exact accounting and preservation

[Field/index accounting](supported-composition-v1-accounting.json) enumerates every
input collection, actual BSON field path/type, source/schema, document-stream
hash and index-stream hash. It includes nested history points and guest layout.
All thirteen entity collections carry a decimal opaque root with no behavioral
meaning. The session's `expiresat` is explicitly normalized to `expires_at` with
unchanged datetime bytes; conversion verification accounts for the transformation.

Regenerate with `go run ./scripts/composition-accounting`. Tests compare it exactly
and require explicit review of changes. This is accounting for the composition's
populated fields, not coverage of all 459 declared legacy fields. The broader
[declaration ledger](legacy-field-accounting.csv) remains separate; its affinity
model-declaration entries do not supersede the explicit affinity state contract.

Original BSON, index metadata and source manifests are read-only during acceptance.
Every part goes through the built offline converter into a distinct staging copy.
Original/index checksums and final staging verification are checked after writes,
restarts and restore. Inert source index metadata is never installed. The owned
current runtime's index definitions are recorded separately and compared across
fresh restore. Mongo's TTL monitor must be disabled before either application boot;
that is a test isolation prerequisite, not evidence for production TTL activation.

## Hosted application gate

The existing `history-acceptance` job runs
`TestHistoryAcceptanceSupportedComposition` against the built Linux application
and offline-converter artifacts with disposable loopback Mongo. The report parser
requires its measurement exactly once alongside every pre-existing acceptance and
refusal measurement. No separate host, workflow dispatch or deployment is needed.

The test bootstraps the composition from the JSON staging snapshot into an owned
empty Mongo database, authenticates and reads inventory, history and tags, edits
VM notes, a group description, a disabled alarm and tags through actual APIs, and
restarts. It checks complete typed records against normalized source BSON, all original
opaque values, exact VM/host associations, history tiers and converted state.
Only the four requested edits and the alarm edit timestamp may differ. It exports the application's backup, verifies checksums and the
feature manifest, restores through application bootstrap into another verified
empty database, takes a second backup and restarts again. Converted affinity and
history state, full tag state, edited configuration, authorization and runtime
index definitions must survive. This proves coexistence of the stated operations;
it does not repeat or expand the separately tested native/guest/alarm behavior.

## Remaining deployed-schema gates

The original fixtures still refuse `unknown_future`, unknown history fields/tiers,
raw unconverted `affinity_rules`, and saved-source raw `vm_tags` or unapproved
history provenance. These are expected negative cases, not reasons to skip this
supported composition.

No active opaque-write/recovery acceptance is added for audit_shipping,
backup_schedule, command_log, discord_gateway, features, hypervisor_certs, idps,
metrics, migrations, notifications, pending_enrollments, server_meta or
snapshot_policies. Native routing/passthrough, affinity name/tag selection and
post-dispatch reconciliation, external alarm delivery, guest installation/growth,
old history aggregation, legacy pruning/TTL and post-upgrade-write rollback remain
outside this gate. Source lineage is not a verified original deployed binary.
Full arbitrary-schema compatibility, native quiescence, UI execution, releases
and deployment remain unapproved.
