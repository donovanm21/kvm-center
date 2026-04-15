---
title: Changelog
section: Reference
order: 90
---

# Changelog

Every tagged release, newest first. Only user-visible changes — test
harness / internal refactors that don't change behaviour are
omitted.

## v5.20.10
- **Fix** — gitleaks kept matching a placeholder bearer-token
  example in `authentication.md` because the assignment shape
  `TOKEN=<base64-ish>` alone satisfies gitleaks' generic-api-key
  rule. Reworded the example to an inline curl with a
  `kvmc_YOUR_TOKEN_HERE` placeholder that doesn't trip the regex.
- **Fix** — the `open-issue-on-failure` Gitea job crashed with
  `KeyError: 'BODY'` on every run because the shell heredoc vars
  weren't exported into `python3 -c`. Rewrote the job to inline
  the whole request flow in a single `python3 - <<PY` block using
  `urllib.request`; no more brittle shell-to-python env bridges,
  and the label-attach failure is now swallowed cleanly so a
  fresh repo without the `security:ci-blocker` label doesn't
  cascade into a red "issue-opener failed" on top of the real
  gate failure.

## v5.20.9
- **Security** — closed every finding run 41 surfaced:
  - **Go 1.25** everywhere (go.mod, Dockerfile.server, both
    workflows). Fixes 24 reachable stdlib vulns that had landed in
    the 1.22 line since we pinned it.
  - **github.com/russellhaering/goxmldsig v1.6.0** — fixes the one
    transitive dep vuln govulncheck flagged.
  - **Retired the shared `migration-keys/` keypair.** The installer
    always generates a fresh keypair per host now; the legacy
    "drop a shared key beside the installer" branch is gone.
    `migration-keys/` is `.gitignore`d so a future key can't land
    in tree, and the repo history was scrubbed of the leaked key
    via `git filter-repo`. **Operators** with fleets that relied
    on the shared key for qemu+ssh:// migration must cross-
    authorize the per-host keys (or switch to qemu+tls:// which
    doesn't need it). See `docs/DEVELOPMENT.md` for the rotation
    steps.
  - **Replaced the realistic-looking token example** in
    authentication.md with an obviously-fake placeholder so
    gitleaks stops flagging it.
  - **Checked in `webui-react/package-lock.json`** so `npm ci` can
    run in CI; unblocks the npm-audit gate.

## v5.20.8
- **Fix** — Gitea Actions runner was cancelling every workflow on
  this repo because it advertised `docker / linux / amd64` and the
  workflow asked for `ubuntu-latest`. Re-registered the runner with
  `ubuntu-latest:docker://catthehacker/ubuntu:act-latest` plus
  `ubuntu-22.04` / `ubuntu-20.04` siblings so GitHub-style workflows
  run unmodified.
- **Chore** — `gofmt -w .` across the tree (28 files) so the
  lint-test gate stays green; no behaviour changes.
- `.gitignore` extended to catch build outputs that previously landed
  at the repo root (`kvm-center-agent-*`, `kvm-tools-*`, `kvmctl-*`,
  and the various `install-*.sh/.ps1` scripts produced by the
  artifacts stage when `go build` runs in a volume-mounted repo).

## v5.20.7
- **Auto-filed issues on CI security-gate failure.** Both workflows
  now add an `open-issue-on-failure` job that fires when a gate
  fails on a push to `main`. A fresh issue is filed with the
  `security:ci-blocker` label, commit SHA, and a link to the failing
  run. Re-failures drop a comment on the existing issue instead of
  opening a duplicate. PR failures are still inline-only.

## v5.20.6
- **GitHub Actions support.** Mirrored `.gitea/workflows/test.yml` to
  `.github/workflows/test.yml` so the same three security gates
  (govulncheck / npm audit / gitleaks) plus gofmt / vet / test run
  on GitHub forks out of the box. The workflow syntax is portable;
  both files are kept byte-identical. Platform notes in
  `docs/DEVELOPMENT.md`.

## v5.20.5
- **CI security gates.** `.gitea/workflows/test.yml` now runs three
  vulnerability scanners on every push:
  - **govulncheck** — blocking on any reachable Go vuln.
  - **npm audit** — blocking on *critical* webui-react deps,
    warn-only on high / moderate / low.
  - **gitleaks** — blocking on any secret match in working tree
    or full git history.
  The `summary` job aggregates the three so Gitea's failure email
  names which gate tripped. Triage playbook added to
  `docs/DEVELOPMENT.md`.

## v5.20.4
- **Deployment page.** Direct Downloads table split into tabs —
  KVM Tools, Hypervisor Agent, kvmctl CLI — so each component's
  artefacts sit under their own heading instead of one long
  15-row list. Added `update-agent.sh` to the Hypervisor Agent
  tab for completeness.

## v5.20.3
- **Fix** — `POST /api/settings/update` no longer clobbers the
  `SetupComplete` flag when the payload omits it. Only the Disable*
  toggles round-trip through that endpoint now; setup state is
  preserved from the current in-memory value. Prevents a stale SPA
  tab from retriggering the first-boot wizard.

## v5.20.2
- **Docs refresh.** README quick start now leads with
  `--interactive`; `getting-started.md` explains the first-boot
  wizard steps + the keep/reconfig/fresh/cancel menu on re-runs;
  `api.md` drops the retired `/api/portal/health` entry and adds
  the new `/api/setup/state` + `/api/setup/complete` endpoints;
  `troubleshooting.md` replaces the old "build portal unreachable"
  row with guidance for the consolidated `/downloads/*` path.

## v5.20.1
- **Installer detects existing state.** `bootstrap.sh --interactive`
  now checks for a prior `.env`, the `kvm-center-mongo` volume, and a
  running stack. If any of them exist it prompts with four choices:
  *keep* (re-run `--deploy` against the current state — safest),
  *reconfig* (regenerate `.env` and wipe the Mongo volume — requires
  an extra confirmation because the old volume's auth will be
  orphaned), *fresh* (destructive nuke: `docker compose down -v`,
  delete `.env` and every `.env.bak.*`, start over — double-confirmed),
  or *cancel*. Hypervisor-side data is never touched by any of these.

## v5.20.0
- **Interactive installer.** `scripts/bootstrap.sh --interactive`
  is a bash+whiptail wizard that prompts for Mongo user / password
  (auto-generates a 40-char secret when left blank), cluster DNS
  domain, and offers to kick off `--deploy` at the end. Falls back
  to plain `read` prompts if whiptail isn't installed. `.env` is
  written with `0600`, gitignored (plus `.env.bak.*`), and the
  captured password variable is unset from the shell so it can't
  leak into `env` / `ps` output.
- **First-boot web wizard.** After the admin account is created,
  the SPA routes to `/setup` and walks the operator through four
  optional steps: welcome, first hypervisor enrollment (mints the
  one-time token + shows the install one-liner), notifications,
  and metrics. Every step has a "Skip the rest" escape hatch so
  the stack is always fully functional even if nothing after the
  admin account is configured. A one-time `SetupComplete` flag
  lives on the Features document and is back-filled to `true` on
  upgrade paths so existing installs don't see the wizard.

## v5.19.0
- **Legacy v3 HTML UI retired.** The React SPA has been the default
  at `/` since v4.0.0; the legacy HTML has now been removed
  entirely. Deleted:
  - the `kvm-center-webui` nginx container and its port **8096**
  - the `webui/` source tree and the `server/static/` embed
  - the "Open legacy UI" entry in the user menu
- `/legacy`, `/legacy/`, `/v4`, and `/v4/` still 308 to `/` so any
  lingering bookmarks land on the SPA.
- One fewer container in the stack, one fewer port to whitelist,
  and the server binary is slightly smaller without the embedded
  legacy assets.

## v5.18.8
- **Fix** — `disable_snapshots` now also gates snapshot policies.
  Before this, flipping the flag in Settings → General blocked ad-hoc
  snapshots but left the policy scheduler ticking and the CRUD
  endpoints wide open, so policy-driven snapshots kept happening
  silently. The scheduler now short-circuits when the flag is on,
  every write on `/api/snapshot-policies{,/…,/…/run}` returns 403,
  and the UI shows a banner + disables New / Edit / Run / Delete
  buttons with a "disabled in General" tooltip. GET stays open so
  the list still renders for audit / read-only reasons.

## v5.18.7
- **Security** — `disable_console` now gates the full console
  surface end-to-end. Before this, only the Launch Console button
  and the console-token mint were blocked; the HTML shell
  (`/api/console/<host>/<vm>`), the WebSocket upgrade
  (`/api/console-ws/...`), and the VM-screenshot proxy
  (`/api/vms/screenshot`) all honoured RBAC but skipped the
  feature flag. A user with `vm.console` could deep-link past the
  UI toggle. All three handlers now call `featureBlocked()` too.
- **Settings tab order.** Re-grouped into three clusters:
  platform basics (General, Database), identity & access (Users,
  Groups, Roles, Identity Providers, API Tokens, TLS), and
  operations (Notifications, Metrics, Audit Log). API Tokens
  moved next to the other auth-related tabs; Audit Log moved to
  the bottom next to the observability siblings.

## v5.18.6
- **Fix** — Snapshot Policies tab blanked out because `usePagination`
  was called after an early `isLoading` return, violating the Rules
  of Hooks. Moved above the guards.

## v5.18.5
- **Pagination on Snapshot Policies.** Same scroll region + footer
  as the rest of the UI for the Settings → Snapshot Policies list.

## v5.18.4
- **Pagination on the per-VM Snapshots tab + Storage page.** Both
  surfaces can grow without bound; they now render inside the same
  scrollable container + pagination footer as the rest of the UI.
  Storage migrated off the legacy controlled `Pagination` shim onto
  `usePagination` for the rows-per-page selector.

## v5.18.3
- **Dark scrollbars + dark dropdowns — everywhere.** Native OS chrome
  on `<select>` and default scrollbars were bleeding through on the
  Audit, Deployment, and pagination surfaces. Applied the strip +
  repaint at `globals.css` so every select gets the dark input box
  with a custom chevron, and every scrollbar (WebKit + Firefox) gets
  a slim dark thumb with an accent hover. No more shiny OS widgets.

## v5.18.2
- **Pagination styling matches the Audit tab.** Dropped the custom
  scrollbar treatment and the custom chevron on the rows-per-page
  selector in favour of the native caret and the plain `bg-input /
  border / 4px radius / 32 px height` input box the Audit tab
  already uses for its filters. Pagination buttons follow the
  same box so the footer reads as one row of identical controls.

## v5.18.1
- **Themed scrollbars + page-size dropdown.** The table scroll region
  now uses a slim, dark WebKit scrollbar with an accent hover state
  and the Firefox thin/colour equivalents. The rows-per-page
  selector loses its native chrome in favour of the house dark
  input style with a custom chevron, so the pagination footer reads
  as one surface instead of a row of OS widgets.

## v5.18.0
- **Scrollable + paginated tables.** Every heavy list (Hosts,
  VMs, Tools update centre, Host detail VMs/Storage tabs) now
  wraps the table in a scroll region with a sticky header row and
  a standard pagination footer: 10 / 25 / 50 / 100 / All, default
  25, first/prev/next/last controls. No more rows getting cut off
  at the bottom of a tall list.
- New shared primitive: `usePagination` + `<PaginationBar>` in
  `components/Pagination.tsx`. The legacy controlled `Pagination`
  component is kept as a thin shim so the Dashboard / Migrations /
  Storage callers keep working unchanged.

## v5.17.2
- **VMs list Guest-tools column.** Replaced the plain text with the
  same tone-pill style as the Tools update centre: `current · vX.Y`,
  `outdated · vX.Y`, `missing`, or `no tools`, with the long-form
  explanation in the tooltip. Same pill on the Host detail VMs tab.

## v5.17.1
- **Host detail de-duped.** The VM list that used to render on both
  Summary and the VMs tab now lives only on the VMs tab, which
  picks up the richer row (per-VM CPU bar + lifecycle + console
  actions) plus the guest-tools column that used to be VMs-tab-only.

## v5.17.0
- **In-UI host sparklines.** Host detail page now shows last-hour
  CPU / memory / aggregate-storage-% sparklines under the resource
  utilisation block. Fed by a per-host in-memory ring buffer
  (120 samples × 30 s) populated alongside the existing Influx
  emit; memory-only, rebuilds on restart. Grafana + Influx remain
  the surface for longer windows, per-VM breakdowns, and anything
  cross-host — the section header links straight to the metrics
  settings tab.
- New endpoint: `GET /api/hosts/<id>/metrics` returns the ring in
  chronological order.

## v5.16.0
- **Consolidated build portal into the server.** The standalone
  `kvm-center-build` container (port **8097**) is retired. Agent /
  kvm-tools / kvmctl binaries and install scripts are now served
  directly by `kvm-center-server` under `/downloads/*`, baked into
  the server image at build time. One fewer container, one fewer
  firewall hole, one fewer URL to remember.
- **Breaking:** `http://<host>:8097/...` URLs in old bookmarks /
  scripts no longer resolve. Update to
  `http://<server>:8090/downloads/...`. `update-agent.sh`,
  `install-agent.sh`, `install-kvmctl.sh/.ps1`, and the kvm-tools
  Windows installer all default to the new path. `bootstrap.sh
  --update-agents` rolls the new URL to every hypervisor
  automatically on the next deploy.
- **Deployment page.** Rewritten to link same-origin
  `/downloads/*`, drop the 8097 health probe, and include kvmctl
  binaries + installers in the direct-download table.

## v5.15.0
- **Docs** — new **Operations** section with two pages:
  *Networking & firewall* (every port kvm-center listens on, the
  firewalld recipe, live/offline migration bandwidth budgets) and
  *Remote access* (VPN / reverse-proxy / Zero Trust topologies for
  publishing the control plane safely).

## v5.14.0
- **In-app docs site.** Markdown files under `server/docs/` are
  embedded into the binary, rendered with goldmark, and served via
  `/api/docs/*`. New `/docs` SPA route with a section-grouped
  sidebar; Help (?) icon in the top bar.

## v5.13.2
- **Fix** — `GIT_SHA` build arg invalidates the `go test` + compile
  layers on every new commit. `bootstrap.sh --deploy` now passes it
  after `git reset --hard`, so a stale cache can't ship green tests
  against broken code.

## v5.13.1
- Grafana dashboard download link in Settings → Metrics. Dashboard
  JSON is embedded into the server binary and served from
  `/api/grafana/dashboard.json` with Content-Disposition: attachment.

## v5.13.0
- **InfluxDB v2 metrics sink + Grafana dashboard template.**
  Background worker ships host + per-VM samples on every telemetry
  tick. Non-blocking enqueue so a slow Influx never back-pressures
  telemetry. Settings → Metrics tab with test-sample button. New
  `metrics.update` / `metrics.test` audit actions.

## v5.12.0
- **Testing foundation.** Seed unit tests for CA signing, Ed25519
  grants, API token hash/lookup/revoke, audit filter, MAC regen XML
  mutation, kvmctl token mask. `make test` / `make lint` / `make
  check` targets. Dockerfile runs `go test ./...` before
  cross-compile so a broken test fails the deploy. Gitea Actions
  workflow mirrors the local gates.

## v5.11.13
- **Fix** — MAC regeneration silently no-op'd on single-quoted
  domain XML. Regex now accepts either quote style; log line
  reports the actual replacement count.

## v5.11.12
- Target host picker: native `<select>` capped at 5 rows with a
  persistent filter field above. Disabled "N more — narrow the
  filter" row when more than 5 matches.

## v5.11.11
- Target hosts sorted alphabetically so telemetry ticks don't
  reshuffle. Filter scans on host ID, IP, CPU model.

## v5.11.10
- MigrateModal pool picker reads the telemetry-backed `/api/storage`
  — same source of truth as the Storage page. Dropped the redundant
  `/api/hosts/pools` server proxy and the matching agent `/pools`
  handler.
- Modal fully resets on close (mode, target, transport, advanced,
  pool, rename, MAC, filter).

## v5.11.9
- **Fix** — offline migrations failed on hosts without a "default"
  storage pool. Agent + UI now pick the destination pool: prefer
  `default`, else first running pool.

## v5.11.8
- Advanced migration section reads as a proper bordered header with
  an accent +/– icon box, subtitle, and a "customised" pill when any
  non-default option is toggled.

## v5.11.7
- **Advanced offline-migration options.** Target storage pool
  picker, VM rename (UUID preserved), regenerate-MACs checkbox (fresh
  52:54:00 OUI per interface). Offline-only; live assumes identical
  layout.

## v5.11.6
- **Offline migration over `qemu+tls` vol-upload.** Replaces scp
  when both hosts are mTLS-enrolled. Disk bytes stream through the
  same libvirtd connection live migrations use — no scp, no
  kvm-migrator keypair on the hot path.

## v5.11.5
- **Fix** — migration record's Transport column hard-coded `"ssh"`
  for OFFLINE_MIGRATE, so authorised offline runs falsely read as
  SSH. Now reflects server-selected or operator-pinned transport.

## v5.11.4
- **Grants on offline migrations + transport toggle.** Offline
  migrations now mint + forward a signed grant like live. Migrate
  modal adds a Transport toggle (Auto / mTLS / SSH); mTLS disabled
  when either host isn't enrolled.

## v5.11.2
- **Fix** — offline migration failed overwriting a running VM's
  disk. Pre-transfer `rm -f` via ssh (kvm-migrator can unlink in the
  group-writable images dir even though qemu:qemu 0600 means it
  can't rewrite the file directly).

## v5.11.1
- **Durable grant replay + libvirt qemu hook enforcement.** Consumed
  grant IDs persisted to `/var/lib/kvm-center/consumed-grants.log`
  so replay rejection survives agent restart. `/etc/libvirt/hooks/qemu`
  refuses out-of-band `virsh migrate` invocations missing a valid
  grant. Agent unreachable fails open so migrations aren't gated on
  agent liveness.

## v5.11.0
- **Signed migration grants.** Ed25519 signer at
  `/var/lib/kvm-center/ca/grant.ed25519.key`. Every mTLS MIGRATE
  mints a grant bound to `{src, dst, vm, 5-min TTL}`; source agent
  forwards to the destination's `/prepare-grant`; destination
  validates offline against the cached pub key and enforces
  single-use. New audit events:
  `migration.grant.issue / .accept / .reject`.

## v5.10.8
- **`install-agent.sh` one-shots mTLS setup.** Socket-activated
  `libvirtd-tls.socket` (not the `--listen` drop-in, which breaks on
  RHEL/Rocky 9+). Firewalld opened for 16514/tcp + 49152-49215/tcp.
  Atomic binary install so re-running against a live host doesn't
  die on "Text file busy".

## v5.10.6
- mTLS pill: green "ok" tone + small lock icon.

## v5.10.5
- **Fix** — mTLS badge never rendered. `time.Time{}` round-tripped
  through Mongo comes back with non-zero wall/ext, so `IsZero()` saw
  every un-revoked cert as revoked. Compare year>1 instead.
- Cert issuance + revocation switched to synchronous `Save()` so a
  restart between response and flush can't lose a cert.
- Migration detail column: human-readable summaries for common
  failures; "completed" instead of `Migration: [X.XX %]…` spam.

## v5.10.4
- **Fix** — live migration left a ghost domain on source. Added
  `--undefinesource` to `virsh migrate`.

## v5.10.3
- **Fix** — CA host lookup tries both short ID and FQDN. Hosts are
  sometimes keyed either way; cert IP SAN was never populated when
  the operator typed the short form.

## v5.10.2
- **Fix** — hypervisor certs include IP + FQDN SANs. libvirt rejected
  `qemu+tls://` dials by IP when the cert CN was the host ID.

## v5.10.1
- **qemu+tls:// migration channel.** Server auto-selects TLS when
  both hosts are mTLS-ready; falls back to legacy `qemu+ssh://` for
  mixed fleets. `install-agent.sh --enroll-token` deposits certs
  into `/etc/pki/libvirt` and flips `listen_tls=1 / listen_tcp=0`.

## v5.10.0
- **Cluster CA + hypervisor identity PKI.** ECDSA P-256 cluster CA
  auto-generated on first boot; 10-year root. Admins mint one-time
  enrollment tokens (10-min TTL); agent exchanges a CSR for a
  1-year leaf cert (CN overridden server-side). Settings → TLS →
  Hypervisor PKI. Audit:
  `ca.enroll / ca.sign / ca.revoke`.

## v5.7.0
- **Per-channel notification routing** — each channel can override
  the global event subscription.
- **Per-collection Mongo writes** — `Save()` hashes each cold
  collection and skips the drop-and-reinsert when unchanged.

## v5.6.0 → v5.6.1
- **User vs system API tokens.** Any signed-in user mints personal
  tokens from the user menu; 30-day default. Admin-only system
  tokens in Settings → API Tokens; default no expiry.
- v5.6.1 fixes the "EXPIRED" misdisplay for no-expiry tokens (Mongo
  round-trip of zero time).

## v5.5.0 → v5.5.4
- **API tokens + `kvmctl` CLI.** Bearer credentials
  (`kvmc_<id>.<secret>`), SHA-256 hashed, shown once. CLI with hosts,
  vms, vm lifecycle, migrate, snapshot create/list.
- v5.5.1 nicer help with colored sections + examples.
- v5.5.2 one-shot installer auto-detects OS + arch.
- v5.5.3 audit log gains a Via column (CLI / WebUI / System) and
  scrolls internally.
- v5.5.4 `kvmctl whoami / config / logout`.

## v5.4.0 → v5.4.7
- **Notifications.** Webhook (Discord / Slack / generic) + email
  channels. Triggers: VM crashed/recovered, host offline/recovered,
  migration complete/failed, TLS cert expiring (<30d), snapshot
  policy failed.
- v5.4.2 Discord/Slack payload shape auto-detection (Discord 400'd
  on raw event JSON).
- v5.4.3 **Fix** — migration complete never fired due to a pointer
  aliasing bug; replaced with a fired-once set.
- v5.4.4 rich Discord/Slack embeds (color-coded by severity, fielded).
- v5.4.5 drain-VMs-on-maintenance UI (server side in v5.4.3).
- v5.4.6 Enter Maintenance dialog promoted to a Modal.
- v5.4.7 event subscription labels include the event key.

## v5.3.2
- Snapshot policies moved out of Settings into a dedicated tab under
  Snapshots. Light/dark parity, tone-pill error surface, button
  white-space fix.

## v5.3.0 → v5.3.1
- Scheduled snapshot policies with retention. Scheduler fires every
  60s; policy-tagged names so manual snapshots are never touched.
- v5.3.1 closed audit gaps around login/console events.

## v5.2.0 → v5.2.2
- Host maintenance mode — refuses new VM placements + migration
  targets. Drain flow is built on this.
- v5.2.1 audit shipping — forward every event to an external
  aggregator (syslog or HTTP).
- v5.2.2 audit coverage gap closures.

## v5.1.x
- MongoDB backend with SCRAM auth from day one. JSON store
  transparent migration on first boot.

## v5.0.x
- Server-proxied VM console (URL sharing is a no-op without the
  session cookie).
- Console HMAC token nonce tracking removed — 5-min expiry is the
  right primary guard.

## v4.0.0 and earlier
- SPA cutover at `/` — legacy HTML UI moved to `/legacy/`.
- Full v3→v4 inventory port; snapshots, migrations, storage,
  hosts, VMs, deployment page.
