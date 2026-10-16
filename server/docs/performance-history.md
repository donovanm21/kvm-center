---
title: Bounded host history
section: Hosts
order: 45
---

# Bounded host history

The server can collect a limited, restart-persistent host history when started
with `-performance-history`. Collection is **off by default**. This slice adds a
read-only API; existing host sparklines and their `/metrics` endpoint are
unchanged. There is no new history chart, VM history, hourly/daily rollup or
year-long retention in this version.

## Collection and retention

History belongs to each Store. It accepts at most one observation per second per
host, using server observation time captured before asynchronous telemetry
processing. Repeated or reordered observations are ignored. The last accepted
second is persisted, so reconnect/reload does not reset this rule. Clock
rollback pauses acceptance for an affected host until server time catches up.

Each host has at most 576 five-minute UTC buckets: the current partial bucket
and 575 preceding buckets. This is a bucket-aligned nominal 48-hour window,
covering between 47 hours 55 minutes and 48 hours as the current bucket fills.
It is not an exact trailing 48-hour interval. Missing samples are not fabricated
or interpolated. Counts describe accepted observations, not elapsed coverage.

The first 64 hosts admitted occupy capacity until their history expires. New
hosts are rejected at capacity; valid history is never silently evicted to make
room. On ingestion, expired entries across all hosts are removed before new
admission. Loading also expires old buckets, after validating all stored data.
Reads filter expired buckets without allocating hosts or modifying dirty state.
If there is no ingestion or restart, expired bytes can remain in storage within
the fixed bounds; this is not a background TTL deletion service.

Each bucket records sample-weighted CPU, memory and storage percentage averages
and peaks, plus the latest observed VM count. Percentages are quantized to
0.0001% before integer accumulation. Values outside 0–100%, non-finite values,
invalid timestamps/identities and invalid VM counts reject the entire history
sample. Storage percentage uses the existing capacity-weighted pool calculation.
With no usable pool capacity, the existing convention is **zero**, which must
not be interpreted as evidence that storage was measured empty.

The state has a 64-host, 576-bucket-per-host bound and an 8 MiB compact JSON/BSON
ceiling. Host IDs are at most 255 UTF-8 bytes. A maximum-count synthetic fixture
with maximum numeric values encodes to 5,954,071 bytes of JSON and 5,578,398 bytes
of BSON. These are serialization measurements, not process RSS or latency
guarantees. The existing short-lived metric rings are separate from this bound.

## Read-only API

`GET /api/hosts/{exact-host-id}/history?range=48h` requires `host.view`, including
the existing scoped-token restriction. Supported ranges are `1h`, `6h`, `24h`
and `48h` (the default); all use five-minute buckets. Unknown hosts return 404,
unsupported ranges 400, and non-GET methods 405. The API does not create history
when reading an unknown host. Unlike the existing sparkline API, there is no
short-name/FQDN fallback.

Responses include `host`, `tier`, `range_hours`, `bucket_seconds`,
`retention_buckets`, `retention_start`, `collection_enabled`, `tracked`,
`active_hosts`, `max_hosts`, `capacity_full`, `last_sample`, `clock_ahead`, and
`points`. Points contain `t`, `n`, `cpu_avg`, `cpu_max`, `mem_avg`, `mem_max`,
`storage_avg`, `storage_max` and `vms`. Times are Unix seconds. A future bucket,
or the last partially future bucket after clock rollback, is withheld from
results. Coverage and capacity metadata must remain visible in any future UI;
an empty series does not mean zero utilization.

Disabling collection still loads, validates, preserves and serves unexpired
history. The response's last observation is not a durable-save acknowledgement.
The existing periodic persistence path saves history with other Store state;
samples since the last successful save can be lost on abrupt termination.
Existing checked saves and the separately opt-in graceful drain remain usable.
No new background worker or independently managed shutdown path is introduced.

## Format, compatibility and recovery

JSON uses the optional `host_history_state` envelope with `version: 1` and a
`hosts` map. Each host contains `last_sample` and `points`. Stored buckets use
integer `cpu_sum`, `cpu_max`, `mem_sum`, `mem_max`, `storage_sum`, `storage_max`
in 0.0001% units, plus `t`, `n` and `vms`; the API derives averages. Mongo stores
the same envelope under `state` in the `host_history_state` singleton document.
Integral BSON doubles are accepted by numeric value; fractional values are not.
Absent state and a version-1 empty host map mean no history. Store snapshots omit
empty state and Mongo clearing deletes the singleton.

Unsupported versions, unknown/duplicate/case-aliased fields, null required
values, invalid buckets and oversized state fail closed. Mongo checks the
namespace before replacing any collection. This remains a preflight, not a
transaction or protection against competing external writers. Dirty hashes are
committed only after successful saves; partial failure retains full retry.
Manual and scheduled archives declare `host_history_state: 1` under
`feature_schemas`, derived from the captured snapshot bytes.

The independent unversioned JSON `history` field or populated Mongo
`perf_history` collection requires a separately reviewed offline converter.
There is no automatic import, field-dropping fallback or migration of a running
deployment. Existing local clients expecting longer ranges cannot use this API
as a drop-in replacement. Older binaries can discard unknown JSON fields or
omit retained Mongo collections from exports; in-place downgrade is unsafe.

Before enabling this on a deployment, retain compatible binaries/configuration,
stop or verifiably quiesce all writers, capture a backend-native backup plus
required application material, and verify isolated recovery. Rollback requires
stopped writers and a compatible pre-upgrade backup. Online restore stays
disabled. The feature flag is not a substitute for these recovery requirements.

## Evidence and remaining acceptance

Synthetic Go tests cover bounds, weighting, ordering, clock rollback, Store
isolation, JSON/backup reload, dirty retry, malformed state and tracked telemetry
work. Hosted Mongo tests cover actual telemetry ingestion, persistence,
connection closure, fresh Store load and an authenticated HTTP read, plus
compatibility rejection before unrelated writes. The browser test uses a real
Mongo-backed server with collection disabled and verifies the API contract; it
does not test enabled collection or a history UI. No new history test mocks a
successful backend response.

Still required before release/operational enablement: sustained ingestion and
save-latency/RSS/disk measurements at the supported bound, native process restart
and interruption acceptance for the exact artifact, isolated restore/rollback,
deployment-lineage compatibility, packaging acceptance and outstanding release
security acceptance. Source/hosted tests alone do not establish full readiness.
