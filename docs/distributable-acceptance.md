# Distributable acceptance and release blockers

The release-readiness audit started at public main `b1caa42b258455870f25e86ba10df02221433017`. Its exact-head and post-merge CI passed 49 browser checks, 31 Mongo checks and six history/recovery outcomes. These are synthetic checks, not deployment acceptance. No release or deployment was performed.

## Packaging gate

`distributable-acceptance` builds the actual `Dockerfile.server` Linux amd64 image from a tracked-source archive on a disposable hosted runner, without registry login or push. It checks the source SHA and embedded version/source stamp payloads and target metadata of the server and ten bundled tool binaries, records their hashes, runs the three Linux tool version-only commands, and exercises the real entrypoint with an isolated named volume and random loopback Mongo database. It checks the non-root server process, Mongo selection marker, authenticated inventory after restart, embedded JavaScript, a downloadable ELF, and healthcheck executable help. HTTP is restricted to loopback; this is not TLS acceptance or fleet health acceptance.

The tag workflow now uses `build-package.sh`, the same fresh-SPA/stamped 12-binary native matrix exercised by history acceptance. Tag/version disagreement fails the shared build. Darwin CLI assets are now included alongside Linux and Windows outputs. Compilation and metadata checks on cross-platform binaries do not establish runtime acceptance on those systems. No installers or hooks execute in this job. The tools now expose a source SHA in their version output; kvm-tools adds an early, side-effect-free `-version` exit. Go omits linker flags from build metadata under `-trimpath`, so runtime output (on Linux) and embedded payload checks are used instead.

The report is `.validation/distributable/report.json`. A passed packaging gate is **not** a release-ready result: the report explicitly retains `release_ready: false`.

## v5.61.58 compatibility is blocked

The preserved source revision `2becda762d2cf55608dc5edd91b3525b0aec7f8b` reports v5.61.58; this does not verify which exact binary is deployed. Public main's version file still reports v5.61.0, so version strings alone cannot establish upgrade ordering. No real database, credential, host identity or private source implementation is copied into CI.

Hand-written synthetic BSON probes use that revision's field spellings. The shared-field codecs now retain VM `host_id`, session `expires_at`, migration `started_at`, and audit `actor_id`, accept their former unseparated public aliases under strict type/conflict rules, and write the canonical keys used by existing indexes. The compatibility probe asserts those four fields survive; it never saves its decoded result. This is not a full-schema converter: typed model writes still discard unsupported fields. See [the codec and preservation contract](canonical-bson-compatibility.md). Public Mongo loading explicitly refuses a populated `perf_history` collection, and the probe verifies that refusal without changing documents.

Additional unresolved differences include alarms, affinity rules, extended VM/host/guest fields and the independent legacy history representation. Separate Mongo collections remaining on disk does not mean the public application can use, back up or restore them. The runtime test deliberately uses the public schema and cannot certify these differences. Reverse rollback also needs a pre-transition backup because the older source does not understand newer public resolution/tag/history state.

Before any deployment: define and independently review an explicit offline conversion/preservation contract, test representative full-schema fixtures and backend-native backup/restore in isolation, verify exact deployed lineage, and obtain approval for the transition and rollback plan. Do not point the public candidate at the deployed database or delete legacy history to get it to start.

## Remaining acceptance

Native hvnode04 and UI lifecycle tests await exact fixture/cleanup approval. ARM64 runtime, Windows/macOS tooling, installation/hooks, long-duration/fleet load and deployment recovery remain unverified. Prior security checks are not final security sign-off; the restricted audit remains stopped. This job neither resumes that audit nor changes host access or trust.
