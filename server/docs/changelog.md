---
title: Changelog
section: Reference
order: 90
---

# Changelog

Every tagged release, newest first. Only user-visible changes — test
harness / internal refactors that don't change behaviour are
omitted.

## v5.37.0 — Tier 2 cleanups + cache-key factory + metrics circuit breaker

A batch of small, mostly-mechanical wins from the Tier 2 backlog.

- **`<StatePill>` + `<LockBadge>` extracted** to `components/StatePill.tsx`.
  Replaces the `<span className={`state-pill ${state}`}>` pattern that
  was duplicated across VMs, VMDetail, HostDetail, Hosts, Dashboard,
  Snapshots overview. Adds aria-labels + leading glyphs so colour
  is no longer the only signal — colourblind operators see the
  same intent without the styling change. (Tier 2 #11 + #14.)
- **`SortableTH` compare is type-aware.** Booleans sort by truth,
  digit-only strings sort numerically, missing values sink to the
  end regardless of direction. Pre-fix every non-numeric coerced
  to string and "10" beat "9". (Tier 2 #12.)
- **Create VM modal resets host-picker on close.** The previous
  pick lingered after dismiss; reopening against an offline host
  surfaced an unending "info loading…" state. (Tier 2 #13.)
- **`parseMigrationProgress` range-check.** Negative or >100
  values now reject instead of leaking into SPA progress bars.
  Test cases extended. (Tier 2 #7.)
- **Error logging on EDIT_VM rollback paths.** When a vCPU cap
  raise fails and the agent tries to bring the VM back up, a
  failed `virsh start` is now logged with the VM name. Pre-fix
  the start error swallowed silently and the operator saw only
  the original setvcpus failure. (Tier 2 #6.)
- **JSON error responses on `/api/hosts/<id>/metrics`.** The two
  `http.Error(w, ...)` plain-text returns are now `writeError`
  JSON. Other `http.Error` sites in oidc/saml/console_proxy/sse
  stay as text — they're pre-handshake or browser-redirect paths
  where JSON would confuse the client. (Tier 2 #10.)
- **Metrics circuit breaker.** Influx push failures past 6
  consecutive opens a 90 s circuit so we stop hammering an
  unreachable server. The producer side still drops on a full
  channel, so telemetry handlers never block. (P2 #4.)
- **`queryKeys.ts` cache-key factory.** New centralised exports
  for every React Query key. Future shape changes (tenant scoping,
  invalidation sets) become one-file edits. Existing hooks
  continue to spell their keys inline; adoption is incremental.
  (P2 #3.)

## v5.36.0 — P0 code-quality fixes from 2026-04-20 review

Five correctness items from the tier-1 backlog, all merged into
one release since each is small in isolation.

- **`Store.ApplyTelemetry` lock discipline.** The telemetry write
  lock is now released explicitly before the metrics + notification
  fan-out goroutines launch. Snapshots of host / VMs / pools /
  transitions are captured under the lock; the fan-out reads only
  those copies. Side effects no longer stall behind the ingest
  path.
- **Migration record torn-write.** `pollMigrationStatus` and
  `finalizeMigration` previously mutated a `*MigrationRecord`'s
  fields outside the lock, then re-upserted via `AddMigration` —
  a concurrent snapshot could see a torn intermediate state. Two
  new store methods (`UpdateMigrationProgress`,
  `FinalizeMigration`) do the field mutation + slice upsert in a
  single critical section. Terminal notification fire-once tracker
  preserved.
- **Agent live-metrics stream cleanup.** `runOneLiveMetricsStream`
  now defers `conn.Close()` AND `stream.CloseAndRecv()` immediately
  after the corresponding resource is acquired. Belt-and-braces —
  the original happy-path pattern was correct, but the paired
  defers mean a future edit can't reintroduce the leak.
- **SPA `any`-typed response handling.** `HostDetail.tsx`'s
  `UpdateAgentButton` declares a typed `UpdateAgentResponse`
  instead of `(data: any)`. Two `(x as any)[sort.key]` escapes in
  the VM + Storage tabs now use `keyof VM` / `keyof StoragePool`.
- **Agent test coverage.** New `collector_test.go` exercises
  `prevVMCPU`'s mutex invariant (concurrent writers + sweepers +
  forgets) under `go test -race`. New `edit_xml_test.go` covers
  the pure-function heart of the EDIT_VM XML-patch path (cpu_mode
  /boot_dev/video_model, multi-quote styles, `<cputune>` vs
  `<cpu>` false-match).

No behaviour change for operators. This is the last of the P0
tier-1 items from the code review.

## v5.35.4 — brisker README tour clip

README hero clip re-recorded at a faster cadence: half the pauses,
quicker scrolls, no host / VM detail detours. Reads as a surface-
area-at-a-glance tour rather than a guided walkthrough. 40 s instead
of 75 s; file size ~2.7 MB instead of 5.7 MB.

The per-feature clips stay at their readable pace — that's where
we want the viewer to follow along, not skim.

## v5.35.3 — back-to-top button on doc pages

Floating "↑ Top" pill appears bottom-right once the reader has
scrolled past the fold on a doc page. Smooth-scrolls back to the
top; resets on every slug change.

## v5.35.2 — right-size walkthrough clips

Clips are quick visual references, not full-screen demos. Two
changes so they read that way:

- Docs prose now caps images to the text-column width via CSS
  (`max-width: 100%` + thin border, rounded corners).
- Re-encoded every clip at 860 px wide (from 1100) so files match
  the display size. Total media footprint dropped ~30 %.

## v5.35.1 — full UI tour in the README

Replaces the old static setup-wizard GIF with a 75-second WebP that
walks every major section of the app (dashboard → hosts → VMs →
snapshots → storage → migrations → System Health → docs). Same
recording pipeline as the per-feature clips; the spec lives at
`demo/walkthroughs/specs/10-full-tour.spec.ts`.

## v5.35.0 — six more walkthrough clips + scroll-restart

Six new operator walkthrough clips, embedded in the docs they
explain:

- **Overview** — snapshot policy create, System Health tour.
- **Getting started** — mint an enrollment token, Create VM wizard,
  configure a webhook channel.
- **Authentication** — filter and inspect the audit log.

Docs viewer now restarts animated WebP clips from frame 0 each time
they scroll into view. The browser otherwise caches the decoded
animation loop, so a user scrolling down to a clip mid-cycle misses
the intro. Implemented in the Docs page with an IntersectionObserver
that swaps the image `src` to a blank pixel when the clip leaves the
viewport and back to the real URL on re-entry — forces a fresh
decode context every time.

## v5.34.1 — walkthrough clips embedded in the existing docs

The in-app docs now ship short silent WebP clips showing common
operator flows, embedded inline where they're relevant:

- **Overview** — a VM lifecycle tour (shutdown → start → snapshot).
- **Getting started** — first-run sign-in + dashboard landing.
- **Migrations** — an offline migration with the progress arc.

Clips are embedded at build time alongside the markdown and served
from a new public endpoint `/api/docs/media/<name>`.

The recording pipeline lives in `demo/walkthroughs/` — Playwright
specs against the demo stack, captured as WebM, then compressed to
WebP via ffmpeg. The whole thing runs in a container
(`scripts/record-walkthroughs.sh`) so the host doesn't need node,
ffmpeg, or the Playwright browsers locally. The demo-walkthrough
source is Gitea-only — scrubbed from the public GitHub mirror.

## v5.33.7 — skill round-trip fixes

Hand-testing the Claude skill bundle surfaced two documentation ↔
server mismatches. Both fixed.

### `GET /api/vms/{uuid}` now exists

`TOOL.md` advertised this endpoint but the server had no per-UUID
handler — clients had to pull `/api/vms` and filter client-side.
Added `handleGetVMByUUID` + `Store.GetVMByUUID` (case-insensitive
UUID compare across all hosts). Registered as a subtree match on
`/api/vms/` so the more-specific exact routes (`snapshots`,
`screenshot`, `notes`, `console-token`) still win. Anything else
under `/api/vms/<...>` is treated as a UUID lookup; unknown UUIDs
return 404.

### `audit_search` path corrected in TOOL.md

TOOL.md said `GET /api/audit/events`; the server exposes
`GET /api/audit`. Pure documentation fix — no server change so
existing SPA callers are unaffected.

## v5.33.6 — install.ps1 ASCII-only

v5.33.5's install.ps1 contained em-dashes (U+2014) in comments +
a warning string. Windows PowerShell 5.1 reads `.ps1` files
without a BOM using the system ANSI code page (CP1252 on English
Windows), not UTF-8 — the multi-byte UTF-8 sequences got
misinterpreted as stray bytes, and one of those bytes happened
to be `"` which closed a string literal early and cascaded into
"Missing closing '}'" parse errors on lines far from the actual
source.

Replaced every em-dash with `-` and swapped one `→` for `->`.
Script now parses cleanly on default Windows PowerShell.

## v5.33.5 — Windows kvm-tools update from the WebUI actually works

Three fixes that together make "Update N selected" on `/kvm-tools`
succeed for Windows guests. Previously it failed with:

```
Copy-Item : The process cannot access the file
'C:\Program Files\kvm-tools\kvm-tools.exe' because it is being
used by another process.
```

### Server picks the right binary by guest OS

`kvmToolsBinaryURL` hardcoded `kvm-tools-linux-amd64`. Windows
guests downloaded the Linux binary and couldn't run it — the
self-update silently stranded. Now `handleGuestTelemetry` passes
the guest's reported OS string ("Microsoft Windows 10 Pro",
etc.) and the URL builder picks
`kvm-tools-windows-amd64.exe` for anything containing
`windows`, defaulting to Linux otherwise.

### `selfUpdate` uses the Windows rename-trick

On Linux `os.Rename(newBinary, exe)` works even for a running
ELF — the kernel keeps the old inode alive. Windows refuses the
rename. But NTFS *does* let you rename a running exe to a
different name within the same directory. The new Windows branch
in `selfUpdate`:

1. Downloads to `exe.new`.
2. Renames `exe` → `exe.old` (running process keeps executing
   from the old file; just on a different path now).
3. Renames `exe.new` → `exe`.
4. Spawns a detached helper (`timeout` + `schtasks /End` +
   `schtasks /Run`) so the Task Scheduler relaunches the new
   binary after the old process exits.
5. `os.Exit(0)`.

Unix path is unchanged — same single `os.Rename` it always did.

### `install.ps1` applies the same trick on re-install

The installer also used `Copy-Item -Force` directly, which hit
the same "file in use" error whenever the script was re-run on a
host with kvm-tools already installed. Now:

1. Stops the scheduled task (best-effort).
2. Renames any existing `kvm-tools.exe` → `kvm-tools.exe.old`
   (works even if the task is still holding the file).
3. Copies the new binary into place.
4. Registers and starts the task. If the copy fails, the old
   binary is rolled back before exit.

### Immediate operator action

Windows guests that were running the pre-v5.33.5 daemon need one
more manual update to pick up the fix — their current daemon
still tries the broken in-place overwrite on self-update. Either:

- Re-run `install.ps1` manually on each Windows guest (picks up
  v5.33.5 `selfUpdate`), or
- Trigger the WebUI "Update N selected" now that the server
  serves the correct Windows binary — the install step runs
  inside the new exe, which has the rename-trick flow.

## v5.33.4 — mobile UI polish

Targeted fixes for the issues reported against the v5.33.0 mobile
breakpoints.

### Table reflow — label above value, not right-aligned

v5.33.0's `table.data-table` mobile rule used `space-between` so
every cell's value got shoved to the right edge of the card. On
rows with long text or inline bars this scrunched content into
an unreadable column.

Now each cell stacks: a small uppercase label row above,
left-aligned value below. Inline bars span the full card width
instead of competing for space. Tables that don't use
`data-label` (VMs list) reflow without the label strip.

### Settings tabs — horizontal scroll instead of clip

The Database / Users / Groups / Roles / Identity Providers tab
bar was overflowing the viewport and clipping tabs off the right
edge. Added `.settings-tabs` to the tab container with
`overflow-x: auto` + flex-shrink: 0 children so the strip
scrolls horizontally on narrow screens while staying snug on
desktop.

### System Health — lightweight mobile view

The dashboard layout from v5.33.1 is great on a laptop but too
much for a phone. On ≤ 640 px viewports, the gauges, CPU/memory
area charts, running-VMs counter, and host breakdown table are
hidden. What remains: the big status tiles, the four subsystem
cards, and the problems feed — a quick-check view similar to
the original v5.33.0 layout.

### Deployment hidden on mobile

Install/download flows aren't phone-friendly. The admin nav
entry now carries `hideMobile: true` and disappears below 640 px
so the drawer is less cluttered for on-call use.

### Multi-column layouts break to single column

Pages that assumed desktop width — VMDetail header (thumbnail +
guest card + usage), Docs (sidebar + content), SnapshotsOverview
(VM list + detail), Dashboard stats bar, per-host card with
Manage + Connected pill — now collapse to single column via a
shared `.mobile-stack` class. Dashboard host card specifically
gets a `.host-card-grid` rule that drops the right column onto
its own row so the Manage link is reachable instead of clipped.

### Horizontal-bleed guardrails

Global `overflow-x: hidden` on body + `.main` + `.panel`, with
`overflow-wrap: anywhere` on code-like strings (UUIDs, migration
IDs, breadcrumbs). Prevents any remaining long string from
pushing the viewport wider than the screen.

## v5.33.3 — fix: gauge sweep direction + orange accent palette

### Gauge sweep — correct fix

v5.33.2 flipped sweep-flag from 1 to 0 thinking the first render
was drawing the bottom half. It was actually correct before; the
apparent "low memory sliver" in the first gauge iteration was the
tiny 8% arc rendering where it should (top-left). Reverting to
`sweep-flag=1` restores proper top-half rendering for both
gauges.

The subtlety: `polar()` in SystemHealth.tsx uses math convention
(angle 90° → point at the TOP of the circle via `y = cy − r·sin(a)`),
but SVG's internal arc-angle measurement uses the raw coord
system where y points DOWN. The point we compute as "math 90°"
is SVG's 270°. Going from the left (SVG 180°) through the top
(SVG 270°) to the right (SVG 0°) is an *increasing* sweep in SVG
terms → sweep-flag=1. Fully commented at the Gauge component
definition now.

### Orange accent everywhere

Gauge fill, CPU area chart, memory area chart, VM-count
sparkline, and the running-VMs big number all switched to
`var(--accent)` (kvm-center orange, `#E3571E` in dark mode, auto-
recoloured in light mode by `globals.css`). Readable against
both theme backgrounds and consistent with the rest of the app's
accent usage.

Hosts-table inline bars keep their green/amber/red threshold
colors — those are semantic status indicators, not stylistic
fills.

### Snapshots overview list stopped bouncing

The "VMs with Snapshots" list on `/snapshots` relied on the API
returning VMs in a stable order. The server's per-host
`vms[hostID]` is a Go map, and iteration order is deliberately
randomised, so every telemetry tick (~15 s) could flip two
entries — Web and TestVM visibly swapped positions mid-use. The
list now applies a stable sort by `(host_id, name)` in the React
memo so positions are locked.

## v5.33.2 — fix: System Health gauges drew the bottom half

The v5.33.1 semicircle gauges used SVG arc `sweep-flag=1`, which
traces the arc clockwise in screen coordinates — i.e. the BOTTOM
half of the circle. At high utilisation the arc looked plausible
but wrong; at low utilisation (memory at 8%) it showed a tiny
sliver sitting below the `0%` / `100%` labels with the body of
the gauge empty.

Fixed by flipping to `sweep-flag=0` (counterclockwise on screen,
which actually traces the TOP half because SVG's y axis is
inverted from math convention). Same fix for both the background
track and the filled-in portion.

## v5.33.1 — System Health dashboard redesign

Reworked the System Health page into a Checkmk-style dashboard
instead of a plain card list. The v5.33.0 shell was a good proof
of concept but didn't give operators the glance-and-triage view
they were after.

### Backend (`GET /api/admin/system-health`)

Response extended with three new top-level fields — old clients
still work, just see more data:

- `aggregates` — fleet-wide numbers: `fleet_cpu_pct`, `fleet_mem_pct`,
  `fleet_vm_count`, `fleet_vm_running`, `fleet_cpu_cores`,
  `fleet_mem_total_gb`, `migrations_today`, `snapshot_blocked`, plus
  a 1h aggregate time-series (`series.t/cpu_pct/mem_pct/vms`)
  computed by averaging the per-host metric ring buffers at each
  slot.
- `host_breakdown` — one row per hypervisor with `cpu_pct`,
  `mem_pct`, `storage_pct`, `vm_count`, `agent_version`,
  `outdated`, `maintenance`.
- `problems` — aggregated "needs-attention-right-now" list:
  offline hosts, outdated agents, expiring certs, stuck
  migrations, stranded command-log entries, snapshot-blocked VMs.
  Each carries `{severity, source, resource, summary}`.

Still cached 15 s server-side; still admin-only (user.manage).

### SPA (`/system-health`)

- **Row 1**: five big status tiles — Overall, Control plane,
  Database, TLS & CA, Hypervisors. Green / amber / red background
  with the status word in 30 px caps. Takes ~1 s to read.
- **Row 2**: CPU gauge (semi-circle, 0–100%) + 1h CPU line chart.
- **Row 3**: Memory gauge + 1h memory line chart (matching
  purple for mem, green for CPU — matches the host sparklines).
- **Row 4**: Running-VM count big-number + VM-count sparkline +
  secondary subsystem cards (Guest tools, Migrations, Snapshots,
  Command log) in an auto-fit grid.
- **Row 5**: Hypervisor breakdown table — per-host inline bars for
  CPU / memory / storage + VM count + agent version with "outdated"
  flag.
- **Row 6**: Cluster problems feed — CRIT/WARN chips, source,
  resource, and summary. Auto-hidden when the list is empty.

Charts are plain inline SVG — no charting library, no bundle
bloat. Area gradient fills match the reference dashboard style.

### Notes

- Per-host ring buffers (1 h / 30 s samples) already existed for
  the HostDetail sparklines; we just reuse them on the backend to
  build the fleet aggregate without adding any new collection
  path. Live numbers update on every telemetry tick; the chart
  fills from the left over the first hour after deploy.

## v5.33.0 — P1: system health + mobile UI + Claude skill

Minor version bump. Ships all three P1 operator-value items from
the roadmap in one release.

### System health page

One URL answers "is the deployment OK?" at a glance. No more
bouncing between Hosts, Migrations, Settings → TLS, `docker ps`,
and Mongo.

- New admin endpoint `GET /api/admin/system-health`. Cached 15 s
  server-side so operator polling doesn't stampede libvirt/Mongo.
- Rollup (green / amber / red) across eight subsystems: control
  plane, database, TLS & certs, hypervisors, guest kvm-tools,
  migrations, snapshots, command log.
- Per-subsystem card: status pill + one-line headline + expandable
  detail map. Amber = degraded but operable; red = action
  required.
- New SPA page at `/system-health` (admin nav). Polls every 15 s.
- Agent telemetry now carries `agent_version`, so the hypervisors
  subsystem flags version drift between control plane and agents.
- Unit tests lock the rollup logic and per-subsystem thresholds.

### Responsive mobile UI

Full parity isn't the goal; the on-call paths (check status, flip
maintenance, power-cycle a VM, tail a migration) now work from a
phone.

- Breakpoints at ≤640 px (phone) and 641–1024 px (tablet).
- Sidebar collapses to an off-canvas drawer below tablet, toggled
  by a new hamburger button in the topbar. Drawer auto-closes on
  route change.
- Data tables reflow to card-per-row stacks below 640 px using the
  existing `.data-table` class — zero per-page code changes. Each
  cell renders its `data-label` attribute as the column header.
- Modals go full-screen below 640 px so create-VM / migrate /
  edit-hardware forms are actually usable.
- Console / noVNC stays desktop-only by design — honest
  disclaimer in situ beats a broken canvas.

### Claude skill bundle

Downloadable skill package that lets Claude Code (or any
Claude-compatible agent) drive kvm-center from a conversation.

- `SKILL.md` — frontmatter + invocation rules + tool reference +
  worked examples. Terse so the model loads it every turn without
  burning context.
- `TOOL.md` — per-tool HTTP schemas + auth + error model.
- `plugin.yaml` — provisional Anthropic plugin manifest.
- `install.sh` — drops everything into `~/.claude/skills/kvm-center/`.
- Bundled at `/downloads/kvm-center-skill.tar.gz`; new "Claude
  Skill" tab on the Deployment page.

Uses the existing kvmctl token file (`~/.config/kvm-center/config.json`)
or `KVMCTL_SERVER` + `KVMCTL_TOKEN` env — no new credential
store. Destructive actions (migrate, delete, power-cycle) require
in-chat confirmation; read-only actions run without a prompt.

## v5.32.21 — Tier S complete: grant rotation + virsh reconcile

Ships the last two Tier S operator-stranding items. The backlog is
now fully cleared — every item the 2026-04-20 stability audit
flagged has a landed fix.

### Grant-signer rotation with grace window (Tier S #9)

A compromise response used to require hand-editing the filesystem
and restarting the server. There is now:

- `POST /api/admin/grant-signer/rotate` (user.manage). Generates a
  new Ed25519 keypair, demotes the current public half to
  "previous", persists both alongside the rotation timestamp.
  Audited.
- `GET /api/migrations/grant-pubkey` now returns `previous_pubkey_hex`
  + `rotated_at` + `grace_expires_at` during the grace window so
  agents can accept grants issued under the previous key for 10
  minutes (longer than the 5-min grant TTL — every in-flight grant
  survives).
- Agent verify path tries current first, falls back to previous
  during the window. On reload, stale previous-key sidecars past
  grace are cleaned up automatically.
- Only the public half of the old key is retained. The private
  half is discarded at rotation, so no new grants can be issued
  under it even during the grace window.

### Virsh drift reconcile (Tier S #1)

Telemetry already replaces the server's VM inventory on every
tick, so routine drift self-corrects. What was missing was an
admin action to **trigger + see** the reconciliation on demand —
essential for post-incident triage when the operator needs to
know whether the SPA is fresh or cached.

- `POST /api/admin/reconcile/{host_id}` (user.manage). Snapshots
  server state, tells the agent to refresh telemetry
  synchronously, snapshots again, returns a diff: added VMs,
  removed VMs, state-changed VMs, and before/after LastSeen.
- New agent endpoint `POST /refresh-telemetry` runs one collection
  + send cycle synchronously and returns when the server has
  applied it.
- Unreachable hosts return a partial report with
  `agent_status: "unreachable"` so the operator sees the problem
  instead of a timeout.
- Audited per call.

### Tests

- `server/grants_rotation_test.go` — rotate keeps previous key,
  previous key still verifies pre-rotation grants, stale
  previous-key sidecars past grace expire on reload.
- `server/handlers_reconcile_test.go` — no-agent-address → skipped,
  unreachable agent → unreachable, missing host → 404, UUID
  indexing falls back to name.

## v5.32.20 — backup covers TLS + CA + grant signer, DR restore works

Before this release, `GET /api/admin/backup` only exported the
store JSON. Everything else needed to rebuild a working control
plane — server TLS material, cluster CA, migration grant signer —
lived in a separate `scripts/kvmc-backup` wrapper that copied
files out of the docker volume. A restore onto a fresh host with
only the API backup left hypervisor enrolment broken: the new
server would issue a new CA, hypervisor mTLS certs stopped
chaining, migrations refused to start. Tier S #11.

Now one tarball is the whole DR story:

- `manifest.json` — version + timestamp + operator identity + a
  SHA-256 checksum per packed file so a restore tool can verify
  integrity.
- `store.json` — unchanged; full persistData export.
- `tls/server.crt`, `tls/server.key` — server TLS leaf.
- `ca/*` — everything under `/var/lib/kvm-center/ca`: cluster CA
  cert + key, Ed25519 grant-signer seed + pubkey. The whole
  directory ships so future additions travel automatically.

Schema format stamp bumped to `persistData.v2`.

Paired with the backup: `scripts/bootstrap.sh --restore <tarball>`
unpacks, verifies every manifest checksum, stops the stack,
copies TLS + CA + store.json into the docker volume's layout, and
prints next-step guidance for the operator (Mongo drop + restart).

### Not in scope

- `.env` (Mongo password) — operator decides to bundle separately
  at restore time; we won't ship database credentials through the
  same HTTPS endpoint that any admin token can hit.
- Per-agent `consumed-grants.log` — lives on each hypervisor, not
  on the server. Rebuilds naturally as new migrations land.

### Tests

- `server/backup_test.go` — `TestHandleBackup_BundlesTLSAndCA`
  drives the handler against a staged temp dir and asserts every
  expected tarball entry is present and every manifest checksum
  matches the packed bytes.
- `server/integration_backup_test.go` updated for
  `persistData.v2`.

## v5.32.19 — durability + rotation + switchover safety

Four small defensive improvements from the Tier S backlog.

### Synchronous flush on terminal migration (Tier S #3)

`finalizeMigration` now calls `store.Save()` directly instead of
relying on the 30-second periodic tick. A server crash between a
migration completing and the next tick used to lose the terminal
flip — the record would still show "running" on restart until the
boot reconciler could confirm. Blocking write here is worth it —
migration terminals are what operators page on.

### Console-secret rotation grace window (Tier S #10)

When the server regenerates the console-token secret (e.g. after
a restore onto a fresh host), agents would reject every existing
token minted with the old secret until their telemetry tick
propagated the new one. A narrow but real window of 401s.

Agents now retain the previous secret for 10 minutes after a
rotation. `verifyConsoleToken` accepts tokens signed with EITHER
the current or the previous secret during the grace window. After
10 minutes the previous secret expires and we're back to strict
current-only verification.

### Backend-switch round-trip verify (Tier S #14)

`migrateBackend` used to Load → Save and declare success; a
partial Save (Mongo write error on a single collection) would
leave both backends in ambiguous states and the operator blind.

Post-Save now reads the destination back and asserts key
collection counts match the source (hosts, users, groups, roles,
hypervisor_certs, api_tokens, snapshot_policies, migrations). Any
count mismatch returns an error from `migrateBackend` so the
caller stays on the source backend. Operator resolves the dst,
retries the switch.

### `migReporter` invariant documented (Tier S #7)

Every read and write of the agent-side `migReporter` global
already ran under `migStatusMu`; the invariant is now spelled out
at the declaration so future edits can't accidentally drop the
lock.

### Tests

- `agent/console_auth_test.go` — previous-secret acceptance
  during grace, rejection after grace, unknown-secret rejection.
- `server/backend_verify_test.go` — matching counts pass,
  missing rows fail, nil read-back fails.

## v5.32.18 — operator escape hatches + defensive boot guards

Four small Tier S operator-stranding fixes. All defensive — no new
user-facing features, each closes a specific way the control plane
could strand state.

### Force-fail stuck migration (Tier S #12)

`handleCancelMigration` refuses to cancel once `phase == cleanup`
(correct for data safety — source disks are gone). But if the
agent crashed mid-cleanup, the record stayed `running` forever
until the next reboot ran the boot reconciler. New admin-only
endpoint `POST /api/migrations/force-fail?id=&reason=` (user.manage
gated) marks the record `failed` with an explicit message and
fires the standard terminal audit + notification. Does NOT contact
the agent — the operator has presumably already verified VM state
out-of-band.

### Phantom host auto-prune (Tier S #13)

A host re-IP'd or retired without an explicit DELETE would sit
`offline` in the store forever, bloating every persistence
snapshot. New `PrunePhantomHosts()` runs hourly (first pass 10min
post-boot) and drops hosts whose `LastSeen` is more than 30 days
old, along with their cached VM and storage entries. Hypervisor
certs for pruned hosts remain — they may still want explicit
revocation through the CA UI, and the cert expiry watcher keeps
tracking them. Each prune fires an audit event.

### Periodic-Save non-empty gate (Tier S #15)

A boot-time Load error (Mongo read-timeout during startup) used to
leave the store empty, which the next 30-second Save tick would
happily write back as empty and overwrite the backend. The
periodic save goroutine now refuses to run until initial Load has
completed — either successfully (any outcome including truly
fresh) or never (stays gated, operator resolves backend and
restarts). Repeated "skipping Save" log lines surface the
condition.

### Singleflight migration poller (Tier S #6)

A new `/api/command` dispatch spawning `pollMigrationStatus`
concurrently with a boot-reconciler pass could double-claim the
same record, both writing to the same `MigrationRecord` and both
calling `finalizeMigration`. Per-ID `migrationPollerClaims` set
rejects the second claim with a log line. Released when the
goroutine returns normally.

### Tests

- `server/migration_poller_claim_test.go` — singleflight and
  independent-ID behaviour.
- `server/phantom_prune_test.go` — expired host pruned with its
  VMs/storage, active host kept, zero-LastSeen skipped.

## v5.32.17 — idempotent commands + bounded retention + SSE hygiene

Rolls three Tier S operator-stranding fixes into one release. The
unifying principle: **every action must be idempotent and
trackable, so reconciliation is easy and precise.**

### Request-ID substrate (Tier S #4) — foundational

Every `/api/command` invocation now carries a `request_id`. The
server records a durable `command_log` entry before dispatching,
the agent keeps a 10-minute outcome cache keyed on the id, and a
retry with the same id returns the cached reply instead of
re-executing virsh. A dropped response + client retry no longer
produces a duplicate snapshot, double migration, or re-applied
EDIT_VM.

- **Client interface**: supply `X-Request-Id` header or body
  `request_id` on `/api/command`. Server mints one if absent and
  echoes `X-Request-Id` on the response. Replays return
  `X-Command-Replayed: true`.
- **Durability**: command log persists through Mongo (indexed on
  submitted_at + outcome, TTL 30 days on terminal entries) and
  the JSON fallback. Survives crash.
- **Reconciliation**: at boot, entries stuck in "pending" for more
  than 45 minutes flip to "unknown" with an audit event. Admin UI
  surfaces them so the operator can resolve manually (the agent
  may or may not have applied the command — server can't tell).
- **Admin API**: `GET /api/admin/command-log?limit=...&outcome=...`
  returns the recent log (user.manage gated).
- **Audit**: every command log state change fans through the
  existing audit pipeline with the request_id in the detail map.

### Migration TTL + slice cap (Tier S #2)

Migration records grew unbounded — `data.json` bloated, every SSE
publish copied the whole slice, `/api/migrations` got slower.

- **Mongo TTL**: completed/failed/cancelled records get an
  `expires_at` stamp (CompletedAt + 30 days) and the `expires_at`
  TTL index sweeps them. Active records never expire.
- **In-memory cap**: `migrationSliceCap = 1000`. When exceeded,
  oldest terminal records drop first. Active records never
  eligible for drop — losing an in-flight migration is worse than
  a memory spike.

### SSE subscriber eviction (Tier S #5)

A stuck reverse-proxy or a suspended laptop could accumulate SSE
subscribers forever, each burning 32-event channel backlog. No cap
existed and slow consumers were never evicted.

- **Subscriber cap**: 256 simultaneous clients. Additional
  connections get `503 Service Unavailable` with a retry hint.
- **Drop eviction**: each `Publish` that can't write to a buffer
  bumps a per-sub drop counter; three consecutive drops → evict.
  Successful writes reset the counter so a subscriber that
  temporarily stalls can recover.

### Tests

New unit tests exercise the core of each change:

- `server/command_log_test.go` — replay detection, outcome
  stamping, Pending age filter, cap eviction preserves pending.
- `server/migrations_trim_test.go` — terminal-only drop,
  all-active no-op.
- `server/sse_eviction_test.go` — stalled subscriber evicted,
  successful write resets counter, cap rejects.
- `agent/outcome_cache_test.go` — round-trip, TTL expiry,
  empty request_id is a no-op.

## v5.32.16 — bootstrap.sh --deploy cleans up after itself

Every `docker compose build` leaves the previous `kvm-center-server`
image layer dangling and adds ~1–2 GB of BuildKit cache. Across
enough redeploys these accumulate — we hit 91% disk on the docker
host before noticing. Now every `--stack` and `--deploy` run ends
with a `post_deploy_cleanup` pass that:

- Runs `docker image prune -f` (dangling only — never touches
  tagged images in use by other stack containers on the host).
- Runs `docker builder prune -f --filter "until=168h"` so back-to-
  back redeploys keep a week of cache for speed but older entries
  get reclaimed.

Skipped automatically on dev workstations where `docker` isn't on
PATH. Picked up by the Gitea CD workflow without changes since it
dispatches through the same script.

## v5.32.15 — cert expiry watcher covers CA + hypervisor leaves

Pre-5.32.15 the expiry watcher only warned on the server TLS leaf.
Two silent cliffs remained: the cluster CA (10-year default —
invisible today, catastrophic on the day it expires because every
migration breaks at once) and each enrolled hypervisor's mTLS leaf.

Changes:

- Watcher now ticks through all three certificate classes every 6
  hours: server TLS, cluster CA, and every non-revoked hypervisor
  cert in `store.listHypervisorCerts()`.
- Two severity bands per cert:
  - **Warning** — leaf <30 days, CA <180 days.
  - **Critical** — leaf <7 days, CA <30 days.
- One-shot edge-triggering per (kind, identity, severity) so a
  cert sitting in the window doesn't spam notifications every
  tick. Certs that rotate past the window re-arm so a later drift
  back in fires again.
- Two new notification types: `ca.expiring` and
  `hypervisor.cert.expiring`. Operators can opt each channel in
  independently from Settings → Notifications.
- Unit tests (`server/cert_watcher_test.go`) lock the edge
  behaviour and per-identity independence.

## v5.32.14 — stopped VMs no longer report phantom CPU / network

Stopped VMs were rendering small but non-zero CPU percentages (e.g.
DRP: 7.8% while SHUT OFF). Three bugs stacked:

- **Agent: no state guard.** `vmCPUUsage` ran `virsh domstats
  --cpu-total` against every domain regardless of state. Network
  ifstats had the same pattern.
- **Agent: uint64 underflow.** When a VM's cpu.time counter reset
  (stop, restart, or driver re-initialisation), the subtraction
  `cpuNs - prev.cpuNs` wrapped to a huge positive number that
  survived the 100% clamp and rendered as a bogus percentage.
- **Agent: stale baselines.** The `prevVMCPU` cache never evicted.
  A VM that stopped, was undefined, or was renamed left a baseline
  that collided with the next start.

Fixes:

- CPU + network counters only read when `vm.State == running`.
- Counter-backwards guard in `vmCPUUsage` drops the baseline instead
  of underflowing.
- Baseline eviction on stop transition (per-VM in `buildVM`) and
  periodic sweep of names no longer in `virsh list --all`.
- Server-side defensive clamp in `ApplyTelemetry`: a non-running VM
  always reports 0 CPU / 0 MemUsage / 0 network bytes regardless of
  what the agent sends. Renders correctly against unupgraded agents.

## v5.32.11 — CI hardens against the bugs this week exposed
- **SPA build gate.** Gitea + GitHub test workflows now run
  `npm run build` (tsc + vite) on every push. v5.30.0 and v5.31.6
  shipped with TypeScript errors that only surfaced at docker-build
  time on the deploy host; now the gate fails before a release tag
  can merge.
- **Migration progress parser test.** `agent/commander_test.go`
  exercises the virsh `Migration: [N %]` scanner end-to-end:
  `\r`-separated input, decimal percentages, pathological single-
  chunk reads. Regression-locks the three bugs from v5.32.8 /
  v5.32.9 / v5.32.10.

## v5.32.10 — live-migration progress scanner splits on `\r`
- virsh `--verbose` separates progress ticks with `\r` (carriage
  return), not `\n`. bufio.Scanner's default split waited on `\n`,
  so every tick between start and finish coalesced into one giant
  "line" and only the first was ever parsed. Custom split on
  `\r`-or-`\n` yields each tick as it's emitted. Combined with
  the v5.32.8 decimal-aware regex, live migrations now stream at
  ~2–10 Hz.

## v5.32.9 — live MIGRATE sets MigrationStatus.Active
- v5.29.1's bootstrap deploy guard and v5.30.0's `/self-update`
  guard both keyed off `MigrationStatus.Active`; only the offline
  flow ever set it. Live MIGRATE ran virsh inline without touching
  the shared status, so an `--update-agents` pass killed any
  in-flight live migration. Source VM survived via libvirt's atomic
  rollback but the server record stranded as `running`. Live now
  owns the status slot for its duration.

## v5.32.8 — live-migration progress regex accepts decimals
- Regex was `\[\s*(\d+)\s*%\]`; virsh emits `[ 0.08 %]`, `[ 1.04 %]`,
  `[100.00 %]` with fractional parts. Zero frames matched until this
  fix, so the gRPC stream was silent for every live migration run
  on v5.32.0–v5.32.7.

## v5.32.7 — live-MIGRATE toast + migrations cache invalidation
- Agent returned the full `virsh migrate --verbose` output as its
  HTTP response body. Server forwarded it to the SPA which showed
  the 5 KB in a success toast. Now respond with a clean "live
  migration completed" summary; granular progress still arrives
  via the gRPC stream.
- `useVMCommand` invalidates the `['migrations']` query on MIGRATE
  and OFFLINE_MIGRATE, so the /migrations page and VMDetail pill
  pick up the new record immediately instead of waiting on the
  60 s refetch.
- Backfilled v5.28.x → v5.31.x changelog entries (the doc jumped
  from v5.27 straight to v5.32.0, missing a dozen releases).

## v5.32.6 — kvmctl ldflag + doc sweep
- `kvmctl version` now reports the release tag (was hard-coded `v5.5.0`).
- Architecture, networking, security docs updated to reflect the `:8090` retirement.
- `docker-compose.server.yml` dropped the legacy HTTP mapping; TLS-only.

## v5.32.0 → v5.32.5 — TLS-only wire + live-migration gRPC + version ldflag

- **Plain-HTTP `:8090` listener retired.** All control-plane traffic
  (UI, hypervisor agents, in-guest `kvm-tools`, `kvmctl`, SSE) now
  goes over the TLS listener on `:8190`. The migration was staged:
  v5.32.0 moved HV agents, v5.32.2–v5.32.4 rotated every guest's
  `kvm-tools` onto `https://…:8190` (self-update path now rewrites
  the installed systemd unit), v5.32.5 flipped
  `KVM_CENTER_DISABLE_HTTP=1` so the server refuses to bind
  `:8090`. Rollback: set `KVM_CENTER_DISABLE_HTTP=0` in `.env`
  and restore the port mapping in `docker-compose.yml`.
- **Live migration progress via gRPC.** `virsh migrate --live
  --verbose` stderr is now tailed for `Migration: [N %]` frames
  and pushed on the existing `StreamMigrationProgress` RPC. SPA
  progress bars update while the migration is in flight instead
  of jumping from 0 to 100 on completion. The offline path, which
  already streamed progress since v5.29, is unchanged.
- **Version ldflag on every binary.** `-X main.Version=$(VERSION)`
  (and `-X main.cliVersion=$(VERSION)` for `kvmctl`) is injected
  at build time by the Dockerfile and Makefile. `kvm-center-agent
  -version` + `kvmctl version` now print the release tag instead
  of `dev`.
- **bootstrap.sh `--update-agents` guard-rails.** Skips any host
  whose `/migrate-status` shows an active migration to avoid
  orphaning a `scp` mid-transfer (the VM2 incident that motivated
  the reconciler probe path in v5.29.2). Override with
  `KVM_CENTER_AGENT_UPDATE_FORCE=1`.
- **Agent `/self-update` guard.** Matches the bootstrap-side guard:
  refuses when `getMigStatus().Active` is `true`; returns HTTP 409
  with the in-flight VM and phase. Override with `?force=1`.

## v5.31.x — polish + operator UX
- **Balloon-stat freshness (v5.31.4).** kvm-center now calls `virsh
  dommemstat --period 5 --live` before reading so the cache refreshes
  every 5 s. Prior behaviour returned week-stale numbers (ark-srv01
  reported 137 MB used while the guest was at 10 GB because the
  `last_update` field was 6 days old).
- **Memory formula (v5.31.5).** VM "used memory" = `actual − unused −
  disk_caches` instead of `available − unused`, which double-counted
  page cache. Old formula showed 45% on a VM really using 20%.
- **Cap-raise modal (v5.31.0).** When the operator tries to raise a
  running VM's vCPU count beyond its `<vcpu>` cap, libvirt's
  refusal now surfaces as a follow-up dialog: "Shut down, raise cap,
  restart" does the whole dance in one click. Opt-in via the modal;
  the raw error is unchanged for anyone scripting against the API.
- **Concurrent-map crash (v5.31.1).** `prevVMCPU` in the collector was
  unguarded; v5.28/v5.29 added two more goroutines hitting it (live
  metrics streamer + lifecycle watcher) which produced a reliable
  "fatal error: concurrent map writes" under load. Mutex added.
- **Grant pubkey TLS (v5.31.3).** `loadGrantPubKey` used a fresh
  http.Client without InsecureSkipVerify, so agents pointed at the
  server by hostname (not the cert's localhost SAN) failed validation
  and the destination agent returned 503 on `/prepare-grant`. Now
  uses the shared `httpClient` which honours the homelab trust model.
- **Host VM list polish (v5.31.6–v5.31.10).** Snapshot button opens
  the name-confirmation modal instead of taking an auto-named
  snapshot; console button routes through `/api/console/<host>/<vm>`
  like VMDetail (was dialling the agent direct, breaking mixed-
  content on HTTPS); sortable headers via the existing
  `components/SortableTH` primitive so the look matches `/storage`;
  rows lock + show the "migrating" pill mid-migration just like the
  top-level `/vms` page.

## v5.30.x — POWER_CYCLE + self-update guard
- **POWER_CYCLE action (v5.30.0).** Post-EDIT_VM "Restart now" used
  to send `RESTART` which maps to `virsh reboot` (in-guest ACPI
  reboot) — the qemu process stays alive so `setmaxmem --config` /
  `setvcpus --maximum --config` never applied. New `POWER_CYCLE`
  action does a graceful shutdown → start, re-spawning qemu. `RESTART`
  is unchanged for anyone wanting a plain guest reboot.
- **Self-update guard (v5.30.0).** Agent's `/self-update` endpoint
  refuses while `migStatus.Active`; returns HTTP 409 with the
  in-flight VM + phase. Matches the bootstrap.sh guard shipped in
  v5.29.1 — closes the remaining way to orphan a migration.

## v5.29.x — migration reconciler + lifecycle watcher + /dist safety
- **Migration gRPC (offline path, v5.29.0).** Server hands the agent
  the generated `migration_id` in the command params; agent opens a
  `StreamMigrationProgress` stream on `startOfflineMigrate` and
  pushes a frame on every `setMigStatus` call. SPA sees
  phase/progress without polling.
- **Lifecycle watcher (v5.29.0).** Agent polls `virsh list --all`
  every 2 s; on any VM state transition it fires an immediate
  telemetry POST so the SPA reflects state flips in ~2 s instead of
  up to 15 s.
- **/dist safety (v5.29.0).** SPA catch-all now returns HTTP 404 for
  paths under `/dist/`, `/downloads/`, `/api/`, `/health`, or with a
  known binary extension (`.amd64`, `.arm64`, `.exe`, `.qcow2`, …).
  Previously a curl-for-binary that got back index.html as HTML
  overwrote the agent on disk during a bad rollout, killing the fleet
  with systemd `Exec format error`.
- **update-agent.sh guard (v5.29.1).** `bootstrap.sh
  --update-agents` probes each hvnode's `/migrate-status` before
  restarting; skips any host with `active=true`. Override with
  `KVM_CENTER_AGENT_UPDATE_FORCE=1`.
- **Reconciler probes source agent (v5.29.2).** Stranded migration
  records that are younger than the 90-min in-flight timeout but
  whose source agent reports `active=false` are now marked failed
  at boot instead of lingering in `running` until the timeout
  expires. Auto-heals the class of bug the v5.29.0 rollout exposed.

## v5.28.x — live VM metrics via gRPC + :8190 multiplex fix
- **Live VM metrics gRPC (v5.28.0).** Each agent opens a persistent
  client-stream over `:8190` (content-type-multiplexed with REST)
  and pushes per-running-VM CPU/mem every ~3 s. Server applies the
  samples to its in-memory VM record and fans out an `inventory` SSE
  (debounced 2 s) so the SPA's React Query cache reflects live
  numbers without waiting for the 15 s HTTP tick.
- **Agent gRPC dial fix (v5.28.1).** `deriveGRPCAddr` pinned the
  port to `:8190` (TLS multiplex) regardless of the agent's
  HTTP serverURL port; v5.28.0 shipped with the live-metrics stream
  dialling the plain-HTTP port where no gRPC listener existed.

## v5.27.0
- **End-to-end integration test suite.** 24 new flow tests under
  `server/integration_*_test.go` exercise the full handler stack
  (auth middleware + RBAC + feature-flag gating + Mongo/JSON
  persistence) as a real client would. Covers register/login/me,
  API token mint-use-revoke, feature-flag preservation on
  `/settings/update`, host delete + its migration-in-flight guard,
  setup wizard public/state, migration reconciler, telemetry
  upsert, backup export round-trip, gRPC migration-progress stream,
  and EDIT_VM / CHANGE_MEDIA permission gates. Regressions in any
  of these land as a CI failure now instead of a user bug report.
- **Backup export API.** `POST /api/admin/backup` (admin-gated)
  streams a `tar.gz` with `manifest.json` + `store.json` — the full
  persistence snapshot. Matches JSONBackend's on-disk format so a
  dump is a drop-in restore source. Full workflow (including TLS +
  CA material) documented in Operations → Backup and restore.
- **gRPC agent channel (foundation).** New `:8095` listener runs
  the `AgentService` gRPC service alongside the HTTP API. The
  `StreamMigrationProgress` RPC lets agents push `virsh migrate`
  progress frames in real time; the control plane updates
  `MigrationRecord` as each frame arrives instead of waiting for
  the synchronous HTTP reply that v5.25.1 had to reconcile. The
  `StreamVMMetrics` and `OpenConsoleSession` RPCs are defined in
  the `.proto` and will be wired in follow-up releases. Disabled
  by setting `KVM_CENTER_GRPC_ADDR=` (empty). See the new
  Reference → Agent gRPC channel doc.
- **Live hardware edits + CD/DVD swap.** Two new agent commands
  (gated on `vm.edit`):
  - `EDIT_VM` — `virsh setvcpus` / `setmem` with `--config --live`,
    falling back to `--config`-only when libvirt refuses live-apply.
    Response reports which changes took effect vs need a reboot.
  - `CHANGE_MEDIA` — `virsh change-media` for live ISO eject /
    insert against a named CDROM target. Takes `media_action`
    (`eject` | `insert`), `target` (libvirt device name like `hda`),
    and `path` (pool-resident ISO, insert only).
  UI surfacing for both deferred to a follow-up release; the API +
  agent paths are live and auditable in v5.27.0.
- **CD safety gate.** `.gitea/workflows/deploy.yml` now only fires
  when the main-branch commit message contains `[deploy]` or an
  operator triggers the workflow manually. Lets dev RCs flow onto
  main for end-to-end testing without auto-redeploying the running
  stack. Promoting to prod is now an explicit operator action.

## v5.26.0
- **Remove host from inventory.** New `DELETE /api/hosts/{id}`
  (gated on `host.manage`) and a **Remove host** button on Host
  detail, next to the maintenance toggle. Removes the host record,
  every VM inventory row, storage-pool metadata, the host metrics
  ring, and the enrolment cert (both short + FQDN keys). Physical
  VMs on the hypervisor are not touched. Refuses with 409 while a
  migration references the host as source or destination. Confirm
  modal makes the operator type the host ID to unlock. Obviates the
  Mongo-surgery + server-restart dance previously needed to retire
  a hypervisor.
- **Snapshot-unsupported badge per VM.** Agent now detects VMs
  whose UEFI pflash nvram is raw-formatted — libvirt refuses
  internal snapshots on those and the cryptic error surprised
  operators. Snapshots Overview shows a yellow "⚠ Snapshots
  unsupported" pill, disables the Create Snapshot button, and uses
  the detected reason as the button tooltip (with a conversion
  recipe). VM detail page surfaces the same in the Snapshots KV
  row plus an extra "Snapshot block reason" row with the full fix.
- **Self-serve agent install via `curl | bash`.**
  `install-agent.sh` now works when piped through stdin: handles
  the unset `BASH_SOURCE[0]` under `set -u`, and if no binary sits
  next to the script it `curl -fsSLk`s the arch-specific binary
  from `$SERVER_URL/downloads/` into `/tmp`. The Deployment page
  one-liner is now fully self-contained.

## v5.25.1
- **Windows kvm-tools install now works end-to-end.** Fixed three
  paper-cuts that all bit the first operator to run the PowerShell
  one-liner against a stock Windows guest:
  - `curl.exe -fsSLk` replaces `Invoke-WebRequest` for the
    bootstrap download. Windows Schannel rejected our self-signed
    cert during handshake before .NET's
    `ServerCertificateValidationCallback` could intervene; curl.exe
    bypasses Schannel entirely. Requires Windows 10 1803+ /
    Server 2019+.
  - Snippet now runs
    `Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass -Force`
    before invoking the downloaded `.ps1`, so Windows' default
    Restricted/RemoteSigned policy doesn't block it. Process-scope
    only — no system-wide change.
  - Stripped Unicode em-dashes (U+2014) from `install.ps1` and
    `install-kvmctl.ps1`. Windows PowerShell 5.1 reads .ps1 as
    Windows-1252 without a BOM, so em-dashes broke the parser.
- **Boot-time migration reconciler.** A server restart mid-migration
  (deploy, crash) no longer leaves records flashing "running"
  forever. On boot the server waits 60 s for telemetry, then for
  each still-running record either marks it complete (if the VM is
  on the destination host) or failed (if older than the 90 min
  in-flight timeout). Fresh in-flight records are left alone.
- **Offline migration snapshot-loss warning.** Offline migration
  rebuilds the VM on the destination from the domain XML + disks;
  libvirt snapshot chains don't follow. The MigrateModal now shows
  a yellow ⚠ row when the VM has snapshots — non-blocking, so an
  operator who captured state elsewhere can proceed. Live migration
  still hard-blocks on snapshots (libvirt refuses).
- **Console viewport fit.** Fixed the noVNC console clipping tall
  guest displays (Ubuntu subiquity installer, etc.). The canvas is
  now CSS-capped to the browser viewport, and a hover-revealed
  "Fit to window" button re-runs `scaleViewport` for operators who
  resize their window mid-session.
- **Event-routing labels.** Per-channel event checkboxes now match
  the general events list (`Display Label (event.code)` instead of
  the code pushed to the right edge of the cell).

## v5.25.0
- **Operator role expanded.** The built-in Operator role now includes
  `vm.create`, `vm.delete`, `vm.migrate`, and `storage.delete` — day-to-day
  VM work no longer falls back to admin escalation for basic provisioning
  and cleanup. Platform-admin permissions (`host.manage`, `user.manage`,
  `tls.manage`, `audit.view`) intentionally stay Administrator-only.
  Existing Operator users pick up the additions automatically on next
  server boot.
- **Complete RBAC permission reference.** Settings → Authentication docs
  now enumerate every permission with what it grants, plus the built-in
  role table. Previously 7 of the 16 non-wildcard permissions were
  undocumented.
- **Grafana dashboard redesign.** The bundled dashboard at
  `/api/grafana/dashboard.json` is now dense and single-import-friendly:
  header with the kvm-center logo, fleet overview tiles (running VMs,
  hosts online, CPU/RAM gauges, free/total RAM), per-host CPU + memory
  trends with clean host-id legends, a scrollable host table with
  gradient-gauge cells, VM state donut, VMs-per-host bargauge, top CPU
  + memory consumers timeseries, and a snapshot breakdown row.
  `bucket` + `prefix` are now textbox variables so Grafana prompts you
  on import instead of silently binding a constant to the wrong value.
- **Interactive installer improvements.**
  - New **HTTPS port prompt** — default 8190, picked port threads into
    docker-compose's port mapping (`KVM_CENTER_HTTPS_PORT` env) and the
    Public URL default in the next prompt.
  - Cross-platform LAN-IP discovery for the completion URL
    (`hostname -I` → `ip route` → `ipconfig getifaddr` → `localhost`),
    so running `--interactive` on macOS no longer silently aborts on
    the cluster-domain prompt or prints a useless machine hostname.
- **Operator documentation updates.** Networking, architecture,
  SECURITY, and remote-access docs rewritten to reflect the v5.24
  TLS-only posture — `:8090` now appears only as a clearly labelled
  backwards-compat listener. Firewall recipe drops 8090/tcp.

## v5.24.0
- **TLS everywhere by default.** Agents, kvm-tools, and install
  snippets now default to `https://<server>:8190`. The plain-HTTP
  `:8090` listener still runs for backwards-compat, but nothing
  advertises it. Install commands pass `curl -k` (and, on Windows,
  a one-line `ServerCertificateValidationCallback`) to tolerate the
  default self-signed cert; each agent report logs a warning until
  you install a trusted certificate via Settings → TLS. Firewalls
  only need 8190/tcp open between hypervisors and the control plane.

## v5.23.0
- **Public URL setting** — Settings → General now has a "Public URL"
  field. Set it once to the canonical address clients use to reach the
  server (e.g. `https://kvm.example.com`) and it threads into every
  install snippet on the Deployment page plus notification webhook
  deep links. `bootstrap.sh --interactive` prompts for it on fresh
  installs and writes `KVM_CENTER_PUBLIC_URL` to `.env` as a seed.
  Leave blank to keep the prior behaviour (snippets built from
  whatever URL you happen to be browsing on, no webhook deep links).
- **Portability scrub** — removed environment-specific defaults from
  shipped scripts and binaries. Agents and kvm-tools default to
  `http://localhost:8090` when no `--server` flag is supplied; the
  cluster DNS domain default is now `cluster.local`; docs and help
  text use `kvm-center.example.com` placeholders.

## v5.20.14
- **CI parity sync** — verified and documented that every security
  fix landed since v5.20.5 applies equally to GitHub Actions:
  - Go 1.25 toolchain: already ported in v5.20.9.
  - Gitleaks history allowlist: `.gitleaksignore` at repo root,
    gitleaks picks it up automatically on both platforms.
  - gofmt / lockfile / package-lock.json / go.mod changes: source
    tree, shared.
  - Posture comment synced at the top of both workflow files.
  The one intentional divergence is the `open-issue-on-failure`
  job — Gitea uses curl + python/urllib, GitHub uses the
  pre-installed `gh` CLI. DEVELOPMENT.md updated to call this out
  so a future editor doesn't try to force byte-identical files.

## v5.20.13
- **Fix** — `gofmt -w server/host_metrics_test.go`. The new file
  landed with a couple of spacing quirks Go 1.25's gofmt rewrote
  (`hostMetricCap / 2` instead of `hostMetricCap/2`); lint-test
  caught it on run 45. The fix is whitespace-only.

## v5.20.12
- **Tests** — added coverage for two modules shipped without tests:
  - `server/setup.go` (first-boot wizard) — state handler before
    and after flip, idempotent complete, non-POST 405, backfill
    skips fresh installs + marks complete when an admin exists.
    Also locked in the v5.20.3 regression fix that preserves
    `SetupComplete` through `/api/settings/update` payloads that
    omit it.
  - `server/host_metrics.go` (v5.17 in-UI sparklines) — ring
    wrap + chronological order, weighted storage-percentage
    across multiple pools, nil-host tolerance, handler envelope,
    405 on non-GET, and the empty-samples path for an unknown
    host ID. Full suite still green.

## v5.20.11
- **Fix** — final gitleaks holdout was a historical commit of
  `authentication.md` where an example bearer token used the
  `TOKEN=<blob>` shape that trips the `generic-api-key` rule.
  HEAD is already reworded; added the finding's fingerprint to a
  new `.gitleaksignore` (one line, fully documented) so the gate
  goes green without a second history rewrite. Real secrets
  should never be allowlisted — rotate + `git filter-repo`
  instead, as the README for that file spells out.
- **Confirmed end-to-end** — on run 43 (v5.20.10) the
  `open-issue-on-failure` job successfully filed issue #1 against
  the repo when gitleaks tripped. The dedup-by-label path will
  pick up subsequent failures as comments on the same issue.

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
