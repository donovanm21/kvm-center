# Changelog

All notable changes to kvm-center, organised by release tag. Dates
reflect when the tag was pushed to origin.

## v5.7.0 (2026-04-14)
- **Per-channel notification routing** — each channel can override the
  global event subscription; "Event routing" accordion inside the
  channel card. Falls back to the global set when the channel's map
  is empty.
- **Per-collection Mongo writes** — `Save()` FNV-hashes each cold
  collection (users, roles, groups, idps, sessions, features,
  notifications, audit_shipping, api_tokens, snapshot_policies,
  server_meta) and only drop-and-reinserts the ones that changed.
  Hot collections (hosts/vms/storage/migrations/audit) still write
  every tick. First save after startup writes everything for baseline.
- Remove stray 💾 emoji from dashboard hypervisor summary.

## v5.6.1 (2026-04-14)
- **Fix** — token list showed no-expiry tokens as EXPIRED because
  `time.Time{}` serialises as `0001-01-01T00:00:00Z` (encoding/json's
  `omitempty` doesn't recognise zero-time as empty). Treat year<1000
  as "no expiry" in both token views; admin create modal adds an
  explicit "no expiry — service account" hint.

## v5.6.0 (2026-04-14)
- **User vs system API tokens.** Split the single tier into:
  - `kind:"user"` — any signed-in user mints for themselves from the
    user-menu → "My API tokens" modal. 7/30/90/365-day presets with
    30-day default. Managed at `/api/tokens/me`.
  - `kind:"system"` — admin-only, created from Settings → API Tokens,
    default no expiry, for service accounts / remote admins.
    Managed at `/api/tokens` (user.manage gated).
  Both inherit owner permissions. Pre-v5.6 tokens backfill to
  `kind:"system"` on load.
- Settings → API Tokens grows Kind + Owner columns.

## v5.5.4 (2026-04-14)
- `kvmctl whoami` — prints user + server + roles + permissions.
- `kvmctl config` — shows config path + server + masked token
  (`--show-token` to reveal).
- `kvmctl logout` — removes `~/.config/kvm-center/config.json`.

## v5.5.3 (2026-04-14)
- **Audit log scroll** — panel scrolls internally instead of bleeding
  past the viewport.
- **Via column** — colored pill distinguishes **CLI** (API token,
  shows token ID) from **WebUI** (browser session) from **System**
  (background tasks). The `via` field is stripped from the Detail
  column so it isn't duplicated.

## v5.5.2 (2026-04-14)
- **One-shot kvmctl installer** — `install-kvmctl.sh` + `.ps1` on the
  build portal. Auto-detects OS + arch, fetches the matching binary,
  installs to `/usr/local/bin` (falls back to `~/.local/bin` when
  unprivileged) or `%LOCALAPPDATA%\Programs\kvm-center` on Windows,
  PATH update, macOS quarantine strip. `--install-dir`, `--bin-name`,
  `--force`, `--portal` flags.

## v5.5.1 (2026-04-14)
- **Nicer `kvmctl` help** — sectioned (AUTH / INVENTORY / VM LIFECYCLE
  / SNAPSHOTS / MISC), color-coded commands + args, usage examples.
  Colors auto-disable when stderr isn't a terminal or `NO_COLOR=1`.

## v5.5.0 (2026-04-14)
- **API tokens + `kvmctl` CLI.** Long-lived bearer credentials for
  scripted use of kvm-center.
  - Wire format: `Authorization: Bearer kvmc_<id>.<secret>`.
  - Secret hashed with SHA-256; shown to the operator exactly once at
    creation. Tokens inherit owner permissions (no per-token scoping
    in v5.5).
  - Audit: every action via a token gets `detail.via=token:<id>` so
    the log distinguishes CLI/CI from browser. Lifecycle
    (create/revoke) audited too.
  - UI: Settings → API Tokens — list + create modal with copy-to-
    clipboard for the secret + revoke.
  - CLI: `cmd/kvmctl` with `hosts`, `vms`, `vm
    start|stop|restart|forcestop|migrate`, `snapshot list|create`.
    Cross-built for linux/macos/windows amd64+arm64 and served from
    the build portal.
- **Audit page size** — clearer hint when the result fits in one
  page so the dropdown doesn't look like a no-op.

## v5.4.7 (2026-04-14)
- Event subscription labels show the event key inline, e.g.
  `VM crashed (vm.crashed)`.

## v5.4.6 (2026-04-14)
- Enter Maintenance dialog promoted to a Modal.

## v5.4.5 (2026-04-14)
- **Drain VMs on maintenance (UI).** Maintenance dialog adds an opt-in
  "Drain VMs" checkbox + Live/Offline radio. Migrations run
  sequentially in the background; progress surfaces in Migrations.

## v5.4.4 (2026-04-14)
- **Rich Discord/Slack webhook embeds** — color-coded by severity,
  fielded layout, footer with version, deep link to the resource when
  `KVM_CENTER_PUBLIC_URL` is set.
- Channel config gains a "Payload format" dropdown (Auto-detect,
  Discord, Slack, Generic JSON) to override the URL-host heuristic.

## v5.4.3 (2026-04-14)
- **Fix migration notification.** `migration.complete` never fired
  because `handleCommand` mutated the same `*MigrationRecord` in
  place before calling `AddMigration`, so the prev-status comparison
  always saw the new value. Replaced with a per-ID fired-once set.
- **Server side of host drain on maintenance** — POST
  `/api/hosts/<id>/maintenance/enter` now accepts `{drain, mode}`;
  sequential migrations off the host via existing plumbing.

## v5.4.2 (2026-04-14)
- **Discord/Slack webhook payload shape** — auto-detect Discord /
  Slack hosts and ship the chat-message envelope they want (Discord
  rejected raw event JSON with 400). Everything else keeps structured
  event JSON for generic receivers.

## v5.4.1 (2026-04-14)
- **Deployment portal probe via server proxy** — the HTTPS SPA's
  direct fetch to plain-HTTP `:8097` was blocked as mixed content,
  surfacing as "PORTAL UNREACHABLE" even when up. Probe goes through
  `/api/portal/health` so the server (plain HTTP capable) does the
  dial.

## v5.4.0 (2026-04-14)
- **Notifications.** Operator-configurable channels + event
  subscriptions.
  - Triggers: VM crashed/recovered, host offline/recovered, migration
    complete/failed, TLS cert expiring (<30d), snapshot policy failed.
  - Channels: webhook (optional HMAC-SHA256 signature header) + email
    via `net/smtp` (global SMTP defaults, per-channel overrides).
  - UI: Settings → Notifications — event-type checkboxes, per-channel
    enable/test/remove, last-send observability with redacted secrets.
  - Every config change + test send audited.

## v5.0.5 (2026-04-13)
- **Server console fix** — dynamic `import()` + correct noVNC module
  path (`core/rfb.js`, not `lib/rfb.js`). Previous version hung on
  "connecting…" because the ES module load silently failed and no
  WebSocket was ever opened.

## v5.0.4 (2026-04-13)
- **Server-proxied VM console** — URL sharing is now a no-op.
  `/api/console/<host>/<vm>` (HTML) and `/api/console-ws/<host>/<vm>`
  (WebSocket splice) both ride the server's HTTPS origin and are
  re-validated against the operator's session cookie on every hit.
  Recipients of a shared URL without that cookie get 401 immediately.
  The HMAC token stays as a defence-in-depth layer — only the server
  ever sees it; the browser never does.
- Stdlib WebSocket splice (handrolled upgrade + dial, raw byte pipe
  once both sides are in WS frame mode).

## v5.0.3 (2026-04-13)
- **Console auth replay guard removed** — the nonce-tracking logic
  consumed the token on the HTML fetch, so the subsequent WebSocket
  upgrade a few ms later always 401'd. 5-minute expiry is the
  appropriate primary guard; one-time-use is a poor fit for the
  HTML-then-WS flow.

## v5.0.2 (2026-04-13)
- **SECURITY: HMAC-authenticated VM console access.** Before this
  patch any LAN client could hit `http://<hvnode>:8081/console/<vm>`
  with zero auth.
  - Server mints short-lived (5 min) HMAC-SHA256 tokens over
    `host|vm|exp|nonce` via `POST /api/vms/console-token` (gated on
    `vm.console` + RBAC + `disable_console` feature flag).
  - Server generates a random 32-byte secret on first boot, persists
    it in the JSON store, and gossips it to agents inside every
    telemetry reply.
  - Agents cache the secret at `/var/lib/kvm-center-agent/console-secret`
    (0600), re-verify every `/console/` and `/ws/` request, 401 on
    any missing/invalid/expired token.
  - noVNC template forwards `token/exp/nonce` onto the WebSocket URL
    so the upgrade re-verifies mid-stream.

## v5.0.1 (2026-04-13)
- **Screenshot proxy** — new `GET /api/vms/screenshot?host=X&vm=Y`
  routes the agent's PNG through the HTTPS origin. VM Summary
  thumbnails no longer trip browser mixed-content blocks now that the
  SPA is served over HTTPS by default.

## v5.0.0 (2026-04-13)
- **TLS support for the web UI** (major).
  - New `-cert` / `-key` flags (plus `KVM_CENTER_TLS_CERT/KEY` env).
    Operator-supplied PEM pair takes priority; otherwise the server
    auto-generates an ECDSA P-256 self-signed cert into
    `/var/lib/kvm-center/tls/` so HTTPS works on a fresh install.
  - `tls.Config.GetCertificate` hot-reloads by mtime — cert swaps
    take effect on the next handshake, no restart.
  - Dual listener: HTTPS on `:8190` (user-facing), plain HTTP on
    `:8090` keeps serving `/api/*` for agents (so they don't need to
    trust the self-signed cert) and 308-redirects browser paths to
    HTTPS.
  - New TLS management endpoints: `GET /api/tls/status`,
    `POST /api/tls/upload` (multipart or JSON), `POST /api/tls/regenerate`,
    `GET /api/tls/cert.pem`. All mutations require the new
    `tls.manage` permission (granted to admins via `PermAdmin`
    short-circuit).
  - **Settings → TLS** tab: expiry countdown, current cert details
    (subject / issuer / SANs / fingerprint / paths), file-upload
    replacement, regenerate-self-signed, download-cert.
- **Agent + kvm-tools TLS tolerance** — when configured against an
  `https://` server, both daemons flip the HTTP client to
  `InsecureSkipVerify` and log a loud startup warning
  (`⚠ TLS verification DISABLED — trusting any certificate`). Acceptable
  on a trusted homelab LAN; flag to audit when we move toward mutual
  auth.
- Docker compose exposes `:8190` TLS + adds a `kvm-center-tls`
  named volume so cert/key material survives image rebuilds.

## v4.9.0 — v4 wrap-up (2026-04-13)
- **Light-mode parity sweep.** Three CSS tone primitives
  (`.tone-pill`, `.tone-panel`, `.tone-icon-tile`) with 5 variants
  each (ok / warn / danger / info / muted) replace hand-rolled pill
  styles across the SPA. Every status badge, banner, and icon tile
  now flips cleanly between light and dark themes.
- `--accent-subtle` gains a light-mode override (brown → peach) so
  hypervisor card icon frames and the Roles admin pill render
  correctly on white surfaces.
- Progress-bar fills switched from hardcoded `#E3571E` to
  `var(--accent)`.
- 15 files changed, -260 / +167 lines — net simplification.

## v4.8.4 (2026-04-13)
- **`install-kvm-tools.sh` fix** — `--uuid` handling was aborting the
  whole script with `UUID_OVERRIDE: unbound variable` when the flag
  wasn't passed (`set -u`). Now defaults to empty.

## v4.8.3 (2026-04-13)
- **Sortable KVM Tools table** — per-column sorting on VM / Host /
  State / Reported version / Status via the shared `SortableTH` hook.
  Default order is `status asc` so warnings surface at the top.

## v4.8.2 (2026-04-13)
- **Resolver Pass 0: trust exact UUID match** before the MAC
  fallback. Prevents the server from overwriting a correctly-pinned
  kvm-tools cache on a guest whose DMI UUID **and** NIC MAC were both
  cloned (ark-srv01's situation), which had caused a cache-oscillation
  loop in v4.8.1.

## v4.8.1 (2026-04-13)
- **Resolver v2: MAC wins over UUID** plus stale-data eviction.
  v4.8.0's scoring made direct UUID match outrank MAC match, which
  attributed cloned-VM telemetry to the wrong domain. New two-pass
  algorithm: MAC match first (globally unique on libvirt), then
  UUID-only fallback. Stale guest_data slots are cleared when a
  collision is detected so the "wrong" VM doesn't show ghosted
  hostname/IPs for 5 minutes.

## v4.8.0 (2026-04-13)
- **Permanent cloned-VM UUID collision resolver.**
  - Server's `ApplyGuestTelemetry` now runs a scored resolver across
    the VM inventory to find the real libvirt UUID a payload belongs
    to. NIC MACs + hostname + UUID + running-state all contribute.
  - Reply includes `canonical_uuid` when the resolver corrects the
    client's claim; kvm-tools caches it at `/var/lib/kvm-tools/uuid`
    and pins that UUID on all subsequent reports.
  - New UUID resolution order in kvm-tools: `-uuid` flag → cache file
    → DMI. `install-kvm-tools.sh --uuid <X>` seeds the cache up front
    as an operator escape hatch for clone-collision cases the
    resolver can't disambiguate.

## v4.7.3 (2026-04-13)
- **Click-to-copy IP badges** — every IP in VM detail (Summary row +
  Networks tab) is now a one-click-copy button with success/failure
  flash. Uses the secure-context-aware clipboard fallback (textarea +
  `execCommand('copy')`) that was introduced for the Deployment page,
  so it works over plain HTTP on the homelab LAN.

## v4.7.2 (2026-04-13)
- **KVM Tools filter polish** — default filter changed from "Outdated +
  missing" (which was empty-by-default) to "All". Renamed to
  "Warnings" (covers outdated + missing + no-tools), added live
  counts per tab. Status column grew a second line describing the
  specific issue per VM.

## v4.7.1 (2026-04-13)
- **Route fix** — `/kvm-tools` is now registered in `App.tsx`. My
  v4.7.0 edit didn't land, so the entry bounced to Dashboard.

## v4.7.0 (2026-04-13)
- **kvm-tools versioning + Update Center.**
  - `server/VERSION` now drives both binaries: `kvm-tools/VERSION` is
    a build-time copy + embed. Every kvm-tools payload carries
    `kvm_tools_version`.
  - New `/kvm-tools` Update Center page lists every VM with its
    reported version + status pill (current / outdated / missing /
    no-tools / queued), with multi-select + one-shot "Update N
    selected".
  - **Self-update:** when the server's telemetry reply includes
    `update_url`, the in-guest daemon downloads the new binary,
    rename(2)'s onto its own ELF, and exits — systemd
    `Restart=always` brings it back on the new version. No SSH
    required.
  - VM Summary's KVM TOOLS pill is a `Link` to `/kvm-tools` and flips
    to amber "v4.x ⚠ outdated" when the reported version lags the
    server.

## v4.6.0 (2026-04-13)
- **Guest interface classification.** kvm-tools now emits per-NIC
  records with a heuristic `kind` bucket: `primary`, `docker`
  (`docker0`, `br-<hex>`), `container` (`veth*`, `cni*`, flannel,
  weave, cali*, podman), `vpn` (`wg*`, tailscale*, `tun*`, `tap*`,
  zt*, ppp*), `bridge` (`virbr*`, `br*`), `other`. New "Guest
  interfaces" panel on VM Networks tab colour-codes them.
- **IP list collapser** — `<IPList>` shows first 2 IPs inline and a
  clickable "+N more" pill to expand. Applies to Summary IP row and
  per-interface Addresses cells.

## v4.5.0 (2026-04-13)
- **Cancel in-flight migrations** with rollback.
  - Agent: `offlineMigrate` takes a `context.Context`, long-running
    `qemu-img convert` + `scp` steps use `exec.CommandContext`. New
    `POST /cancel-migrate` signals the goroutine; the existing
    rollback path restores the source VM.
  - Server: `POST /api/migrations/cancel?id=<...>` gated on
    `vm.migrate`. Flips the record to `cancelling`, proxies to the
    agent, finalises as failed once the agent returns. Refuses to
    cancel once phase reaches `cleanup` (source disks already
    removed).
  - For live migrations, falls back to a best-effort
    `virsh domjobabort` on the source.
  - UI: Cancel button on every active migration card (MigrationCenter
    + Migrations page). Disabled past the cleanup phase with a
    tooltip explaining why.

## v4.4.0 — v4 wrap-up milestone (2026-04-13)
- Multi-step **VM delete** now has an explicit "Also delete the
  backing disk files" checkbox (default off). Agent honours
  `delete_disks=true|false` and logs when disks are intentionally
  kept.
- **VMDetail Actions menu** locks when the VM has a running migration
  on the source host, or a failed migration whose phase reached
  `define`+ on the destination. A "migrating" / "rolled back" pill
  renders next to the state pill.
- **Storage page** paginates at 15 pools/page, matching VMs / Hosts /
  Migrations.
- Pagination is now a shared component with proper light-mode
  styling.
- New `.lock-pill` and `.topbar-mig-badge` CSS classes with
  light-mode variants.
- VMDetail summary shows a "**KVM TOOLS: not installed**" pill
  (greyed) when no guest agent is reporting.
- **CHANGELOG.md** added (this file).

## v4.3.1 (2026-04-13)
- VM and Host list rows are fully clickable (whole `<tr>` navigates
  to detail).

## v4.3.0 (2026-04-13)
- **Migrations page** gets free-text search across VM / source /
  destination, Status + Mode dropdown filters, Clear button, and
  paginated history at 15/page. Active migrations are pinned above
  the filters so they're never hidden.

## v4.2.3 (2026-04-13)
- MigrateModal "Run in background" / backdrop / Esc all redirect to
  `/migrations` so the operator can't accidentally fire follow-ups
  against the in-flight VM.

## v4.2.2 (2026-04-13)
- VMs list **locks the source row** while a migration is running
  and (if the rollback may have left dest artifacts) **locks the
  destination row** after a failed migration.

## v4.2.1 (2026-04-13)
- Server rejects `MIGRATE` / `OFFLINE_MIGRATE` with 409 when another
  migration is in flight on the same source or destination host.
  MigrateModal mirrors the gate client-side with a "Host busy"
  banner.

## v4.2.0 (2026-04-13)
- **Server auto-records migrations.** Handler creates a
  `MigrationRecord` on every MIGRATE / OFFLINE_MIGRATE and (for
  offline) polls the agent's `/migrate-status` until terminal —
  fixes the long-standing "migration not in summary page" bug.
- New **MigrationCenter** modal listing all in-flight migrations,
  reachable from a pulsing topbar badge that appears whenever
  `active > 0`.
- MigrateModal stays open after submit with a live progress card;
  "Run in background" minimises while the migration continues.
- Heading polish: drop trailing `(N)` counts on VM/Host detail and
  Snapshots overview.

## v4.1.3 (2026-04-13)
- `server/VERSION` file is now the single source of truth (embedded
  into the binary via `go:embed`, also stamped into the build-portal
  page).
- Dashboard sorts hypervisors by id (stable across SSE ticks) and
  paginates at 10/page.

## v4.1.2 (2026-04-13)
- Drop the `-X main.Version=...` Docker override so the committed
  const isn't clobbered with `dev`.

## v4.1.1 (2026-04-13)
- Dashboard KPI cards: replace `auto-fit` grid with
  `repeat(4, minmax(0, 1fr))` so all four cards stay equal width.

## v4.1.0 (2026-04-13)
- **Sortable headers** on Hosts / VMs / Storage (new `SortableTH`
  component + `useSort` hook).
- **Editable VM notes** inline editor wired to `POST /api/vms/notes`
  (gated on `vm.edit`).
- VMDetail Summary: right columns aligned across rows, Launch
  Console bottom-aligns with the info panel.
- Snapshot Revert/Delete buttons use inline SVG icons.

## v4.0.0 — v4 cutover (2026-04-12)
- **React SPA mounted at `/`**; legacy v3 SPA preserved at `/legacy/`.
- Full v3 → v4 port: Dashboard, Hosts, VMs, Storage, Snapshots,
  Migrations, Settings (General / Users / Groups / Roles / Providers),
  Deployment.
- Self-service topbar: user menu, logout, theme toggle, TOTP 2FA
  enrolment.
- Create VM wizard.
- Hypervisor Summary with meta row, big-stat cards, utilisation bars,
  embedded VM table.
- Remote file browser inside the Storage view.

---

## Pre-v4 (legacy v3 series)

The v3.x line was the Python-era UI + the initial Go rewrite milestones
before the React SPA cutover. Retained here for reference; the full
details live in the Gitea commit log.

- **v3.7.3** — last point release before the v4 cutover.
- **v3.7.0 → v3.6.0 → v3.5.0** — progressive v3 feature releases.
- **v3.4.0 → v3.3.0 → v3.2.0 → v3.1.0** — mid-v3 polish.
- **v3.0.0** — initial Go rewrite + legacy SPA baseline.
