# Release validation and evidence

A successful compile, a mock-agent test, and a real VM migration prove different
things. Do not call the product “100% stable” or “all features validated” from any
one of them. The report records what ran, what failed, and what did not run.

## Commands

Prerequisites: Go 1.26.9+, Node ^20.19.0 or >=22.12.0, Python 3, Bash, Git; a C compiler for the Go
race detector. Use a writable Go/npm cache when the environment has no home
folder. The commands never push, deploy, contact a homelab, or install software
system-wide.

```sh
make validate                 # harness, format, module consistency, vet, race tests,
                              # form/schema/type regressions, production SPA, 12 cross-compiled binaries
make validate-mongo           # real Mongo tests; requires local disposable Mongo
make validate-browser         # tests an already running local disposable demo
make validate-release         # every gate; incomplete acceptance exits nonzero
python3 scripts/validate.py --profile packaging
```

Each invocation creates a new `.validation/<timestamp>-<pid>/report.json` and
per-stage logs. It records commit, dirty state, a fingerprint of actual source
bytes, durations, exit codes and test counts. A source change during the run
invalidates the result. Aggregate success stays false until the run finishes
and the final source identity matches. Existing evidence directories are never overwritten.
`--output /path/to/new-directory` and `--timeout 1800` are supported.

`passed`, `failed`, `blocked`, `running`, and `not_run` are distinct. Missing
executables/services, zero-test integration runs, skipped integration tests,
flaky/skipped browser tests, stale SPA output, and timeouts cannot become passes.
Normal unit-test skips are listed; they do not imply platform coverage.

`selected_gates_passed` describes only the selected profile. The generic runner
always leaves real-hypervisor acceptance blocked: it deliberately cannot grant
itself permission to mutate hosts or accept a hand-written “passed” evidence
file. `release_gates_passed` therefore remains false until an authorized lab
acceptance workflow is implemented and reviewed. This is a visible unfinished
release requirement, not a switch to disable for a release.

### Real MongoDB

Start a disposable, unauthenticated MongoDB on loopback, then:

```sh
export KVMC_TEST_MONGO_URI=mongodb://127.0.0.1:27017
make validate-mongo
```

The `integration` build tag separates these tests from ordinary `go test ./...`.
They use random `kvmc_validation_<random>` databases, and drop only their own
database in cleanup. Missing configuration or connection failure fails instead
of skipping. Do not use a shared or production database. The current tests prove
reconnect/load/save, inventory, auth state and scopes, BSON-only secrets, deletion,
false boolean persistence and dirty-collection selection. They do not yet prove
replica-set failover, cross-collection atomicity or crash recovery under load.

### Browser + real server + Mongo fixture

```sh
npm ci --prefix demo/e2e
cd demo/e2e && npx --no-install playwright install chromium webkit && cd ../..
KVMC_TEST_MONGO_URI=mongodb://127.0.0.1:27017 scripts/run-browser-fixture.sh
```

The fixture builds and embeds a fresh production SPA, seeds a uniquely named
local Mongo database, and runs a compiled server bound to loopback TLS port
18290 (`KVMC_TEST_PORT` overrides it). It disables fixture notification channels
and snapshot policies and replaces every synthetic host endpoint with
`127.0.0.1:1`. No synthetic agent is started. Tests check the server's embedded
Git SHA against a clean committed candidate. Dirty trees are rejected, and
the fixture freezes/rechecks revision and cleanliness around the build and run.
Browser retries are zero; a flaky retry cannot
hide a failure. Cleanup stops the server, drops only the random fixture DB,
and removes generated CA/private keys and binaries; logs remain in the printed
fixture directory. Review traces before sharing because they can contain
session information, even though fixture accounts contain no production data.

If MongoDB is unavailable, `KVMC_TEST_BACKEND=json scripts/run-browser-fixture.sh`
uses a new private JSON file instead. The report labels the backend `json` and
inventory `synthetic`, and the browser asserts the actual backend type. This
proves browser/API/JSON behavior only. It never satisfies the separate Mongo
integration gate. These fixture commands are manual-only; no Mongo/browser CI job is activated by this change.

For an existing **local disposable** seeded demo, set `KVMC_DISPOSABLE_DEMO=1`
and `KVMC_DEMO_URL=https://127.0.0.1:<port>`, then use `make validate-browser`.
External URLs, embedded credentials, multi-host URIs and Mongo proxy/driver options are rejected. Mongo fixture connections force direct mode so replica discovery cannot redirect writes. The browser fixture uses
real API, auth, Mongo and SPA rendering, but its inventory is synthetic. It does
not establish real VM lifecycle, migration, console or storage correctness.

### Release artifacts

`make release-binaries` builds and embeds the SPA before producing server and
agent Linux amd64/arm64 binaries; guest-tools Linux amd64/arm64 and Windows amd64;
and CLI Linux amd64/arm64, macOS amd64/arm64 and Windows amd64. Checksums enumerate
only this matrix, not stale files. The release tag must match `server/VERSION`.
Version injection uses `cliVersion` for CLI and `versionRaw` for binaries that
initialize `Version` from the embedded file. The build stamps server `GitSHA` explicitly and disables implicit Go VCS
discovery so isolated worktrees/build contexts do not resolve an unrelated
parent `.git` directory.
Cross-compilation proves compilation, not execution on the target OS/CPU.

The Docker/download bundle additionally requires these sources, which were
absent at the reviewed baseline `564a6c8`:

- `scripts/update-agent.sh`
- `scripts/install-kvmctl.sh`
- `scripts/install-kvmctl.ps1`
- `scripts/libvirt-hooks-qemu.sh`

Recover and review their canonical sources. Do not replace them with stubs,
remove the copies to make a build green, or claim deployment readiness while
the advertised downloads are missing. `.gitignore` now permits these canonical
script paths; root-level generated copies remain ignored. The manual packaging
profile fails early on missing inputs. Native container build is another
separate gate; successful native build is not multi-architecture runtime QA.

### Real-hypervisor acceptance

Only after explicit authorization, use a disposable multi-host lab with known
rollback/cleanup plans, test-only credentials and no valuable guest disks:

```sh
scripts/e2e-hypervisor.sh --list  # no token or network required
# Authorized lab only; mutates VMs, pools, users, policies and host settings:
KVMC_TOKEN=... KVMC_SERVER=... KVMC_HOST=... KVMC_PEER=... \
  scripts/e2e-hypervisor.sh --strict --with-retire
```

`--strict` rejects skipped scenarios; a zero-pass run fails. Retirement/re-enrolment
requires `--with-retire` and can disrupt hosts. The existing cleanup is best-effort;
independently verify no test VMs, disks, pools, identities or flags remain, and
record any leftovers. Record server/agent versions and SHA, host OS/libvirt
versions, transport, per-scenario outcomes, logs and before/after inventory.
Do not treat browser tests against no-op demo agents as this acceptance.

## Feature validation matrix

“Existing evidence” below means a test source exists, not that a particular
candidate passed. Consult its report. Rows intentionally expose unvalidated
paths; this matrix is a minimum acceptance backlog, not an exhaustive API count.

| Surface | Existing executable evidence | Evidence still needed for a release |
| --- | --- | --- |
| First-run setup, local auth, sessions, tokens, RBAC/user/group/role changes | `server/integration_{setup,auth,user_mgmt}_test.go`, token/scope tests; browser auth | Real provider SSO (OIDC/OAuth/SAML/LDAP), TOTP recovery, restart persistence and least-privilege browser journeys |
| Dashboard, host/VM lists and detail, health, docs, phone routes | `demo/e2e/specs/{inventory,system-health,docs,mobile}.spec.ts`; exact-revision assertion | Empty/error/loading states, Back/Forward and stale-request cancellation across every page; accessibility |
| VM create/start/stop/restart/delete/edit/autostart | Agent commander/action/outcome tests; server VM-edit/command tests | Real libvirt scenarios C/D/V, repeated clicks, concurrent users, interrupted operations and cleanup |
| Console and VNC/websocket access | Agent console-auth and server grants tests; lab I1 | Actual display stream, token expiry/revocation/replay, viewer permission and disconnect/reconnect |
| Storage pools, file browser, upload/download, disk deletion | Agent filebrowser/validation tests; lab B/ES1 | Real pool/storage backends, traversal and partial-transfer behavior, permission failures and disk safety |
| Snapshots and scheduled policies | Server integration flows + agent command unit tests; lab N/P | Real online/offline snapshots, revert failure, retention, policy scheduler and cleanup |
| Migration, maintenance and drain | Server migration/maintenance tests, agent mTLS/SSH tests; lab G/H/F | Two actual hosts, live/offline shared/nonshared storage, mTLS/SSH modes, cancellation, restart and lost-reply recovery |
| Host enrollment, retirement, PKI, certificate rotation | Server enrollment/CA/grants/host-retire tests; lab A/K/M | Real installer hooks, approval/rejection, actual restart/rotation and trust revocation on all supported hosts |
| Guest tools and CLI | CLI tests, 12-target cross-build matrix; lab T | Run binaries on supported guest OS/architectures; verify upgrade/download/install/rollback/version behavior |
| Backup/export/restore, persistence/backend change | Server backup/persistence integration and unit tests; real Mongo round-trip tests | Recover into an isolated instance, verify every field and identity, failed write, disk full, interruption and backend switch; unsupported restore must stay disabled |
| Settings, TLS, notifications, audit shipping, metrics, Discord | Server settings/audit/Discord/metrics tests; lab S/NT/AS/DG | Real test destinations with consent, failed delivery/retries, redaction, restart persistence and TLS browser trust |
| Async tasks, reconciliation, cancel, SSE/cache refresh | Server reconciler/progress/task/SSE tests and agent durable-outcome tests; lab CC/MG/RC/TS | Agent/server restarts, interrupted replies, repeated request IDs, conflicts across hosts, multi-session convergence |
| Packaging, dependencies and security | Source matrix, packaging preflight, npm audit, govulncheck, gitleaks CI | Restore missing release sources, native/multi-arch container build + runtime smoke, full target-OS execution and operator acceptance |

## Bounded fix → test → review loop

1. Freeze a candidate and run source gates. Preserve failures and exact evidence.
2. Pick one reproducible blocker. Add a regression that fails before the fix;
   make the smallest change, then run focused tests and all affected gates.
3. Review the diff for authorization boundaries, data loss, concurrency,
   compatibility and masked failures. Re-run the full source profile on the
   final combined candidate, not only individual worktree patches.
4. After three unsuccessful fix/retest cycles for the same blocker, stop that
   line of work and report reproduction, attempted fixes, remaining risk and
   required decision. Continue independent authorized checks. Do not remove
   assertions, introduce retries, skip failing tests or broaden privileges.
5. Run real Mongo, browser, packaging/security and authorized lab acceptance.
   Missing runtime, credentials, permission or canonical source remains a
   blocker. Never convert it to a pass based on a mock or stale result.
6. Publish/merge/deploy only with the required authorization, exact-candidate CI
   and reviewed acceptance evidence. Source success is not deployment approval.

## Active CI and proposed gates

Browser and Mongo fixture tooling remains manual-only. Existing GitHub jobs run:
Go formatting/vet/unit tests and vulnerability scan, form/schema/type regressions,
production frontend build and audit, and secret scanning. Those jobs do not establish race, Mongo, browser,
container or hypervisor acceptance. No new dispatch, schedule, hosted service,
workflow permission or publication action is activated here.

Future CI activation requires a separately reviewed change and approval for the
disposable services and runner resources. The intended gates are source/race/
cross-build, real Mongo, Chromium/WebKit fixture and release-input completeness.
Every required job must report success; cancellation, skips, stale revisions and
missing evidence must block. The supplied CI-result helpers can check exact-SHA
runs but are not wired into release workflows in this commit. Real-hypervisor
work must remain in an explicitly authorized isolated lab.

## Recorded adoption status (16 October 2026)

This is an evidence snapshot, not a release certificate. Public baseline
`42bbf010842396560476f284a98f2aa4f5d2b5aa` contains reviewed changes
[17](https://github.com/donovanm21/kvm-center/pull/17),
[18](https://github.com/donovanm21/kvm-center/pull/18),
[19](https://github.com/donovanm21/kvm-center/pull/19),
[20](https://github.com/donovanm21/kvm-center/pull/20), and
[21](https://github.com/donovanm21/kvm-center/pull/21).

| Gate | Status and exact scope |
| --- | --- |
| Public source correctness | Passed full Go race suite on the combined command/backup/disk source and patched dependencies; PR 21 Node 20/Go/build/security CI passed. Candidate-specific reruns remain mandatory. |
| Desktop browser | Passed 16 Chromium checks with no retries/skips on local candidate `fb007c5` (tree identical to public baseline): real server/session/JSON, synthetic inventory, exact-SHA assertion, navigation and read-only modal dismissal. |
| Mongo persistence | Not validated against a live Mongo service. Integration source is present; BSON/mock tests cannot replace runtime evidence. |
| Mobile/WebKit | Blocked in the available environment by missing runtime libraries. No successful mobile-browser claim. |
| Release packaging | Blocked by four absent installer/hook inputs. Canonical sources were located, but recovery alone is insufficient; compatibility and safety review are required before restoration. |
| Containers and target OS runtime | Not validated. Cross-compilation is compilation evidence only. |
| VM lifecycle, disk/pool operations, real console and migration | Unit/handler coverage only; real multi-host acceptance has not run. Disk deletion remains disabled where ownership cannot be established. |
| Backup and disaster recovery | Failure regressions pass; online restore is explicitly unsupported. Live Mongo crash consistency and complete isolated restore remain release blockers. |
| Identity, trust, upgrades and recovery | Security acceptance remains incomplete. Require reviewed boundaries, secure bootstrap, supported mixed-version behavior and a tested recovery procedure before adoption. |

Do not release or deploy on the strength of a source/browser pass. Preserve
failed and blocked reports. A regression after integration requires reverting
or isolating the responsible code on a reviewed branch, rerunning affected
gates, and validating recovery before retrying. Stop on unexpected targets,
ambiguous ownership, data loss, changed trust, new permission requirements or
unexplained state divergence; do not weaken tests, TLS or access controls.

## Staged KVM control-plane roadmap

The target is a vCenter-like alternative for KVM. Use the matrix above to drive
incremental acceptance rather than adding large unvalidated feature surfaces.

1. **Safety and reproducibility first.** Close identity/auth boundaries, durable
   command outcomes, persistence/backup gaps and release-source omissions. Pass
   source, Mongo and browser gates on the final combined candidate. Keep unsafe
   recovery actions disabled until implemented and validated.
2. **Trustworthy single-host operations.** Validate VM lifecycle, edits,
   snapshots, consoles, pools, disks and network attachment on a disposable
   real host. Include interruption, repetition, RBAC denial and resource cleanup.
   Add network/bridge/VLAN inventory and lifecycle coverage before calling
   networking equivalent to a mature virtualization control plane.
3. **Multi-host operations.** Validate maintenance/drain and live/offline
   migration across supported storage and transport combinations. Prove conflict
   handling, reconnection and reconciliation after server/agent interruption.
4. **Enterprise operations.** Complete provider-backed identity/RBAC, usable
   audit/export, metrics/alerts and operator diagnosis. Prove delivery failures,
   permission boundaries and UI state convergence with real test destinations.
5. **Recovery and lifecycle.** Restore into a clean isolated environment and
   verify complete data/identity recovery; exercise database outages and failed
   writes. Validate supported server/agent/guest-tools upgrade, downgrade and
   rollback combinations plus installer integrity on each target platform.
6. **Release candidate acceptance.** Freeze versions and run every applicable
   matrix row, security scan and target-platform runtime check. Record known
   limitations and rollback plans, have independent review, then seek explicit
   release/deployment approval. Never infer approval from green checks.

Each stage depends on the previous stage's evidence. Feature work is scoped only
after the existing stability blockers are closed; this plan is not permission
for a feature rewrite or for deployment.
