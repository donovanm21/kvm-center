# MongoDB startup selection

When `-mongo-url` (or `KVM_CENTER_MONGO_URL`) is configured, MongoDB is authoritative. The server never falls back to a local JSON file on readiness, load or legacy migration failure. A populated file may be stale; its existence is not recovery evidence.

Readiness gets five attempts, each with a 10-second context and a three-second delay between attempts. Each failed client is closed with a two-second budget. Bootstrap has a 110-second overall context, including the legacy load/migration stage (at most 30 seconds). Actual Store hydration is separately bounded to 15 seconds. Failure exits nonzero before HTTP/gRPC listeners and background workers start. Logs report the failed stage without printing raw connection errors or the supplied URI.

After successful Store hydration, the server atomically records `mongo` in `<data-file>.backend`, compatible with the existing private checkout's marker format. If this marker says Mongo but the Mongo configuration is missing, startup stops rather than selecting JSON. Unreadable or unrecognized selection markers also stop JSON selection. There is no fallback override. Restore the intended database configuration and restart through the normal approved recovery process.

Intentional JSON-only installations without a prior-Mongo marker, or with an explicit `json` marker, retain JSON selection. Changing a Mongo installation to JSON requires an offline, reviewed data/backend transition; removing the marker is not a data recovery procedure. The existing Settings database-copy API does not update this selection marker.

Legacy JSON-to-Mongo migration still uses the existing zero-host heuristic. This patch does not establish that the heuristic is sufficient or that migration is transactional. Migration and source-archive failures now stop startup. A failed migration may have written part of the destination; inspect and restore a verified backend snapshot before retrying if necessary. Never deploy an older fallback-capable binary as recovery for a database outage.

Validation uses synthetic backend failures, actual-main child processes with invalid configuration, and a disposable loopback Mongo database for retry/restart persistence. No production outage, live deployment, host mutation or restricted security audit is part of this change. Native recovery and deployment acceptance remain separate release gates.
