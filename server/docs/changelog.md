---
title: Changelog
section: Reference
order: 90
---

# Changelog

User-visible changes, grouped by minor version and newest first,
following the [Keep a Changelog](https://keepachangelog.com)
conventions. Patch releases are folded into their minor version;
internal refactors, test, and CI changes are omitted. See the git
history for per-commit detail.

## v5.61

### Added
- Opt-in `-performance-history` collects bounded host history for a read-only `host.view` API: at most 64 hosts and 576 five-minute buckets per host, with versioned JSON/Mongo/backup persistence. Collection defaults off; longer tiers and a history chart remain future work.
- VM detail pages on desktop and mobile can create, assign, edit and remove tags. Conditional API writes wait for checked persistence, surface conflicts and support identical retries after unconfirmed saves; VM list searches match tags. Reads require `vm.view` and writes require `vm.edit`.
- Versioned VM tag persistence foundations cover snapshots, JSON/Mongo reload and backup archives. Tag APIs and UI remain future work; older binaries require offline recovery from a compatible pre-upgrade backup rather than an in-place downgrade.
- Servers can opt into a bounded 20-second shutdown drain with `-graceful-shutdown` after establishing exclusive backend writer ownership. Accepted work and persistence are joined before backend closure; incomplete drain exits unsuccessfully. A successful drain is not a consistent backup guarantee.
- The agent has an opt-in fixture-only validation runtime with separate temporary state, loopback listeners and no native host workers or command routes. It does not yet validate native VM operations or enrollment.
- Administrators can record an observed success or failure for a stranded (`unknown`) command through `POST /api/admin/command-log/resolve`. Resolutions retain the actor and note, wait for persistence before acknowledgement, and prevent late agent outcomes from replacing the decision.

### Fixed
- Server Compose services use an init process and a bounded in-process TLS health probe, avoiding external TLS helper processes. Health status alone does not automatically restart an unhealthy container.
- MongoDB saves replace retained records without clearing the collection first. Obsolete users relinquish usernames needed by recreated identities; retained IDs are never deleted by that cleanup. Saves remain nontransactional across records and collections.
- Backups and JSON persistence retain all control-plane state, including pairing and enrolment credentials, without exposing private fields through API model JSON. Configured security material must be readable before a backup succeeds.
- Failed persistence writes retain their pending changes for retry; taking a backup no longer consumes pending database updates.
- Online restore is temporarily disabled to protect live state until a backend-aware transactional restore workflow is available. Offline recovery requires a stopped service and a verified backend-specific plan.
- Agents load durable command history before accepting commands and stop safely when operation history is unavailable or corrupt. Competing cancellation/completion updates preserve the first terminal result.
- Commands with the same request ID execute once while in flight and replay recorded outcomes after agent restarts. Unavailable operation storage now blocks dispatch safely.

### Security
- VM deletion retains disk images and attached media. Automatic disk cleanup is disabled until exclusive storage ownership can be verified; API requests with `delete_disks=true` fail before changing the VM.
- The server→agent command channel is now authenticated. The server signs every command request and agents reject unsigned or forged commands once the upgraded server advertises signing. Rollout is automatic and lockout-proof; `KVM_CENTER_DISABLE_AGENT_CMD_AUTH=1` is an emergency kill-switch.
- Agent self-update verifies an Ed25519 signature on the downloaded binary before installing it, and only updates from the configured server — closing an unsigned-binary code-execution path.
- SSH host-key checking defaults to `accept-new` (trust-on-first-use) rather than accepting any key; full strict checking stays available via Settings.
- VM names are validated and shell-quoted on the migration path, closing a command-injection surface.
- The storage browser is confined to real storage-pool paths.
- Documentation is rendered without raw-HTML passthrough (defense-in-depth XSS hardening).
- `kvmctl` and kvm-tools gain a TLS-verification opt-in (`KVMCTL_VERIFY_TLS=1`, `KVM_TOOLS_VERIFY_TLS=1`).

### Changed
- kvm-center is now open source under the Apache License 2.0, with a published container image, a `NOTICE` file, and public contributor documentation.
- The server container runs as a non-root user. On upgrade, the entrypoint repairs data-volume ownership automatically — no operator action needed.

## v5.60

### Added
- Retired-hosts panel in Settings → TLS, with one-click un-retire.
- Recent Tasks now includes server-side operator activity (host retire/maintenance, enrolment approve/reject, snapshot-policy and identity/RBAC/notification changes), not just VM operations.
- Create-VM host picker is now a searchable typeahead that scales to large fleets.
- One-line Windows install command for kvm-tools.
- 365-day retention window for audit events.

### Changed
- `/health` returns HTTP 503 when the system is critically unhealthy, 200 when degraded or OK; body keeps `status` and adds `overall` and `version`. Suitable for k8s / uptime liveness probes.
- Agent and kvm-tools upgrades stage the new binary and automatically roll back if it fails to start.
- Install scripts surface the server's actual error instead of a generic failure, and check prerequisites up front.
- Hosts, VMs, Storage, Migrations and guest-filesystem tables sort stably — rows no longer reshuffle on each telemetry refresh.

### Fixed
- Create-VM and Storage-browse no longer crash on hosts with no ISOs, bridges, pools, or files; empty states render cleanly throughout.
- Discord Gateway no longer needs re-pairing after a restart or deploy, and auto-recovers if a pairing stalls.
- "Failed events only" audit-shipping toggle keeps its unchecked state.
- Live and offline drain migrations succeed on fleets without shared storage.
- Cluster state persists reliably under heavy load.

### Security
- API-token scope is now enforced on `/api/command` — a read-only token can no longer drive lifecycle actions.
- 2FA changes and permission denials are recorded in the audit log.

## v5.58

### Added
- Storage-pool lifecycle management for directory pools (define, start, stop, autostart, delete) from the API, unblocking the Create-VM wizard on hosts with no pools defined.

### Changed
- Editing both vCPU and memory caps on a running VM in one submit now applies in a single reboot instead of two.

### Fixed
- TLS migrations to hosts enrolled via the approval flow now succeed; their certificates include the host's IP and short-name SANs. (Existing approval-flow hosts need to re-run `install.sh` to pick up a corrected certificate.)

### Removed
- The "while you were away" task toast on tab-focus; the Recent Tasks pane remains the single surface for in-flight and completed activity.

## v5.57

### Added
- Stranded-VMs banner on the host-detail page when an offline host still has VMs last-seen on it.
- Configurable phantom-host prune retention in Settings → Features (default 30 days, clamped 1–365).

### Changed
- The expected-down toggle is now always visible, so a host can be flagged for planned downtime before it goes dark and suppress the offline alert from the first missed telemetry tick.

## v5.56

### Added
- Operator-acknowledged "expected down" state for hosts: flag a planned downtime with a reason and optional return ETA to suppress offline paging, with an audit trail; the flag auto-clears when telemetry resumes.

### Fixed
- After reinstalling a host, cross-host SSH (and the migrations that rely on it) no longer break on a stale host key — the fleet's known-hosts entries refresh automatically.

## v5.55

### Added
- Hypervisor enrolment approval flow: enrol a new host without minting a token. The installer submits a CSR and shows its public-key fingerprint; the operator verifies and approves it under Settings → TLS → Pending enrolments. The token-based enrolment path remains available for scripted/IaC use.
- `KVMC_AUTO_APPROVE_CIDR` skips manual approval for enrolment requests from a trusted subnet (off by default).
- Pending enrolments panel in Settings → TLS, with a count badge, live refresh, full request details (host, source IP, fingerprint, version, system info), and Approve/Reject controls.
- New pending-enrolment requests fan out as Discord audit cards when the gateway is enabled.

### Changed
- Newly enrolled hosts are automatically meshed for SSH migration: each host's `kvm-migrator` public key propagates fleet-wide within an hour, and retired hosts drop out on the next cycle — no manual `authorized_keys` editing.
- Repeated enrolment attempts on the same host automatically supersede earlier pending requests, so only the newest one needs operator attention.
- The create-VM wizard now offers OS-level bridges (e.g. a `br0` bridged to the physical NIC) alongside libvirt-managed networks, so LAN-routable bridges are no longer hidden.
- The installer now defaults the host ID to the full hostname the agent reports, preventing duplicate host records on enrolment.

### Fixed
- Approved hosts are now recorded in the Hypervisor PKI panel and treated as mTLS-ready immediately after enrolment.
- A VM no longer stays locked for up to 30 minutes after a fast, deterministic failure from the agent; such failures now resolve immediately so the next action can proceed.
- Editing a VM with shutdown allowed no longer reports a false failure while the operation actually succeeds; the operator now waits for and sees the agent's real result.

## v5.54

### Added
- Host retire flow: removing a host now fully retires it — the server tells the agent to stop and clean up, then blocks the retired host from re-enrolling or silently reappearing via telemetry. Previously "deleted" hosts kept coming back.
- Un-retire control under Settings → TLS clears a host from the deny-list so it can be freshly enrolled again.

### Changed
- The Remove-host confirmation now offers a "remove agent binary + cert files" option (on by default) and a free-form reason that is captured in the audit log; the audit entry also notes whether the agent stopped cleanly.

## v5.53

### Added
- Discord Gateway audit firehose: an opt-in mode that ships audit-log entries to Discord as cards, with a "failed events only" filter for a quieter incident-flavoured stream. Configured under Settings → Notifications → Discord Gateway.

### Fixed
- The noVNC console no longer hangs at "connecting…"; the console page now loads its viewer and establishes the connection.
- Lowering a running VM's RAM below its current cap now prompts to power-cycle so the new cap actually takes effect, instead of silently appearing applied.
- The hypervisor enrolment installer now parses the signing response correctly across RHEL/Rocky/CentOS, so new hosts can enrol via the documented one-liner.
- The Discord Gateway no longer reports a misleading healthy badge while every send fails; it now surfaces a clear "re-pair required" state when its credentials are missing.

## v5.52

### Added
- Discord Gateway integration: kvm-center can fan notifications into a shared Discord audit channel through a paired gateway service, with no per-source bot token. Configured under Settings → Notifications → Discord Gateway via a pairing handshake; a status badge surfaces connection health, and a bounded send queue reports dropped events during an outage. Discord notifications are advisory — the canonical record stays in the audit log.
- In-app Discord Gateway operator guide at Docs → Operations → Discord Gateway.

### Changed
- The Discord Gateway TLS toggle now reads "Skip TLS verification" (checked = skip) and defaults to skip, matching the gateway's stock self-signed cert; Re-pair applies the current form values, and a TLS-error banner offers a one-click "disable TLS verification + re-pair" fix.

## v5.51

### Added
- Fine-grained API token scopes: a token can now carry an explicit subset of its owner's permissions (e.g. read-only or snapshot-only). Scopes can only narrow, never elevate. Available when minting both system and personal tokens; leaving scopes empty keeps the existing inherit-owner-permissions behaviour. Scoped-token actions record their scope in the audit log.

### Fixed
- Audit-shipping and scheduled-backup settings now survive a restart on Mongo-backed deployments instead of being lost on every redeploy.

## v5.50

### Added
- Live-inventory status pill that pulses amber while reconnecting and red when closed, so stale data is no longer silent.
- Group membership management under Settings → Groups: a Members count column and a Members checklist in the edit modal, with every membership change captured in the audit log.

### Changed
- Reverse-proxy setup guidance for nginx, Caddy, and Traefik v3, plus a "bring your own infrastructure" section covering external MongoDB and operator-supplied TLS certs.
- Destructive-action confirmations now use a consistent, keyboard-accessible modal across the app.
- Restore via `bootstrap.sh --restore` now writes reliably into the Docker named volume on standard setups.

### Fixed
- The KVM Tools table no longer clips rows on hosts with many guests; the same fix preempts the issue on the VMs, Hosts, Storage, and Snapshot Policies pages.
- A broken lazy-loaded page no longer blanks the whole app; an error boundary contains the failure.
- VM list and audit changes (RAM edits, telemetry refreshes, VM creation) now reliably persist instead of surviving only in memory until the next restart.

### Security
- Per-request body-size caps on every `/api/*` handler to defend against single-request memory exhaustion.
- Security response headers (HSTS, Content-Security-Policy, X-Frame-Options, Referrer-Policy, nosniff) on every response.
- CORS allowlist (`KVM_CENTER_CORS_ORIGINS`) replaces the previous wildcard origin.
- Trusted-proxy CIDR list (`KVM_CENTER_TRUSTED_PROXIES`) so `X-Forwarded-For` is only honoured from trusted peers and source IPs can't be spoofed.
- Demo-mode lockdown (`KVM_CENTER_DEMO_MODE=1`) blocks admin restore/restart/grant-rotate and redacts client IPs; the demo API token now expires after 26h.
- LDAP bind passwords are now encrypted at rest, with existing records re-encrypted on first save.
- Agents can optionally verify the server's TLS certificate (`KVM_CENTER_AGENT_VERIFY_TLS=1`).

## v5.49

### Added
- kvm-center is now published on Docker Hub as a signed, multi-arch image (`linux/amd64` + `linux/arm64`), pullable in one line with rolling (`:latest`), minor (`:5.49`), major (`:5`), and immutable per-release tags.
- Pre-built KVM appliance qcow2 on every GitHub release: import it with `virt-install --import` for a working control plane in minutes, with a one-shot first-boot service that generates an admin password and prints it to the console. Appliance images are CentOS Stream 9 based and ship with `SHA256SUMS` plus a GPG-detached signature; container images are cosign-signed, with verification keys and steps in the docs.
- Deployment docs gain a Watchtower auto-update snippet, a release-notification systemd timer, and a consolidated "Verifying releases" section covering cosign and GPG.

### Changed
- During a migration the VM list shows a single row throughout instead of briefly duplicating the VM, and the kvm-tools view now applies the same migration lock (badge, no bulk-select, no click-through) as the main VM list.

### Fixed
- The "while you were away" task toast only fires on a true terminal outcome (success, failed, cancelled), no longer announcing completion prematurely mid-migration.

## v5.48

### Added
- Central Recent Tasks pane on every screen: the last several actions with action, target, live progress bar, outcome, and duration, plus a click-through detail modal.
- Real-time progress for long-running actions, streamed from agents and rendered live in the Tasks pane and command log.
- Cancel a running action directly from the UI, with graceful termination and migration abort.
- Per-VM in-flight indicators and conflict detection, so a second action on a busy VM is reported instead of silently colliding.
- Auto-resume banner: tasks that finish while a tab is hidden surface their outcome when you return.
- Dashboard activity widget with a 24-hour sparkline, running and failed counts, and average duration per action.
- Opt-in success notifications per operator, with failure and cancellation notifications on by default.

### Changed
- Slow actions (large snapshot deletes, migrations) no longer hold an HTTP request open; the agent accepts them asynchronously and the server reconciles the final outcome, so the UI stays responsive.
- The app shell now keeps the topbar and sidebar fixed while only the content area scrolls, with the Recent Tasks pane spanning the full width along the bottom.
- Scrollbars are themed to the brand palette with a stable gutter, so content no longer shifts when they appear; tables render at their natural height with column headers staying pinned while scrolling long lists.

### Fixed
- A wedged or crashed action is now resolved to a clear final outcome (success, failed, or unknown) instead of appearing stuck or stranded in pending forever.
- Long tables (e.g. Migrations) render all their rows rather than clipping the list to a fraction of the viewport.

## v5.47

### Added
- A single unified Linux installer (`install.sh`) replaces the separate agent and guest-tools scripts, auto-detecting the right mode; the Deployment-page snippets are true one-liners again. Legacy install URLs keep working.
- Read-only Snapshots section on the mobile VM detail screen, newest first with the current snapshot pinned.
- Full keyboard navigation and ARIA labels on the actions menu, and visible focus rings across inputs, selects, and buttons.
- New troubleshooting runbooks (maintenance-mode patching, agent-offline recovery, forgotten-admin-password recovery), an Upgrading guide, TLS certificate-lifecycle notes, expanded API reference for user/group/role/permission endpoints, and backup/DR documentation.

### Changed
- Sort order on the Hosts, VMs, Storage, and KVM-tools tables now persists in the URL and survives navigation.
- Toasts are de-duplicated, so a flapping endpoint no longer stacks identical errors.
- The setup wizard's final step and the empty Dashboard now offer next-step guidance and a deployment-guide link.
- The web UI loads noticeably faster, with admin-only pages split into their own chunks to shrink the initial download.
- Long-running actions whose initial response is lost now show as pending and resolve to their true outcome, instead of being incorrectly marked failed.

### Fixed
- A restore can no longer overwrite the live TLS cert with an invalid one or run while a migration is in flight, preventing a bad backup from bricking the HTTPS listener.

### Security
- The plain-HTTP listener is now off by default; opt back in with `KVM_CENTER_LEGACY_HTTP=1` only if you still run pre-v5.32 guest tools.
- gRPC client-certificate enforcement defaults on for fresh installs; existing fleets keep their current setting and can opt in from Settings.
- Session cookies are marked `Secure` over TLS, repeated failed logins are throttled with a temporary lockout, and agent telemetry is attributed to the real connection address rather than a spoofable payload value.

## v5.46

### Added
- New app-like mobile web experience for phones under a `/m` route: read-only fleet overview, host list and detail, VM list and detail, and an activity feed, with iOS safe-area handling and add-to-home-screen (PWA) support that launches straight into the mobile shell.

### Changed
- Mobile lists keep a stable order across refreshes instead of reshuffling on every poll, with consistent spacing across screens.

### Fixed
- The mobile bottom tab bar now stays anchored at the bottom of the viewport on long pages.
- Mobile screens render at 1:1 zoom on load instead of appearing shrunk, with long values wrapping cleanly.
- Windows guest-tools self-update now reliably relaunches on the new binary via a Task Scheduler watchdog. Windows hosts on v5.46.4 or earlier must re-run `install.ps1` once for the new trigger to register; subsequent updates are automatic.

## v5.45

### Added
- Scheduled backups support 5-field cron expressions in addition to fixed hourly intervals, selectable from a toggle in the UI.
- Backup tarballs can be encrypted with an AES-GCM passphrase; restore decrypts automatically when the passphrase is supplied.
- Operators can distribute a curated SSH `known_hosts` file to all agents from the server, which agents fetch at startup and hourly.
- Web console sessions can be opened over the gRPC transport.

### Changed
- The topbar and sidebar now stay pinned while long pages (Docs, Audit log, System Health) scroll, so navigation stays in view.

### Fixed
- State pills no longer render a duplicate status dot.
- The Dashboard "connected" status pill now shows its running colour.
- Windows hosts no longer lose kvm-tools after a server-triggered self-update.

### Security
- gRPC client-certificate (mTLS) enforcement can be required, refusing any caller without a CA-signed client certificate.

## v5.44

### Security
- gRPC client-certificate (mTLS) authentication is now available as an opt-in; once an agent has its leaf certificate, gRPC streams authenticate via the cert automatically, with no operator action required. Defaults to the existing posture.
- Strict SSH host-key checking for migrations is now an opt-in (Settings → General), off by default.

## v5.43

### Added
- Built-in backup scheduler: enable recurring backups with an interval, destination path, and retention count from Settings → Backup → Schedule, with a last-run summary. Disabled by default.

## v5.39

### Added
- Restore from a backup archive directly in the web UI (Settings → Backup): upload a `.tar.gz`, contents are verified against the manifest, and a typed confirmation plus a server restart bring the system back on the restored data. The CLI restore path is unchanged.

## v5.37

### Changed
- Status pills carry accessible labels and leading glyphs so state is conveyed without relying on colour alone.
- Sortable table columns sort by type: booleans by truth, numeric strings numerically, with missing values sorted last.

### Fixed
- The Create-VM host picker resets when the modal is closed, so reopening no longer gets stuck loading host info.
- Migration progress values outside 0–100 are rejected instead of distorting progress bars.
- Failed VM restarts during an edit-VM rollback are now logged with the VM name.

### Security
- gRPC streams now require a recognised host identifier, so unknown TCP peers can no longer open a stream and inject frames.
- Migration audit events record the transport (auto/mTLS/SSH) and mode (live/offline), making SSH-vs-mTLS migration choices visible in the audit log.

## v5.35

### Added
- Doc pages gain a floating "Top" button that appears after scrolling past the fold and smooth-scrolls back up.

## v5.33

### Added
- New System Health dashboard at `/system-health` answers "is the deployment OK?" at a glance: fleet-wide status tiles, CPU and memory gauges with 1-hour trend charts, a running-VM counter, a per-hypervisor breakdown table, and a live problems feed (offline hosts, outdated agents, expiring certs, stuck migrations, snapshot-blocked VMs). Admin-only.
- Responsive mobile UI: the sidebar collapses to a drawer, tables reflow to stacked cards, and modals go full-screen so on-call paths (check status, flip maintenance, power-cycle a VM, tail a migration) work from a phone.
- Downloadable Claude skill bundle lets a Claude-compatible agent drive kvm-center from a conversation, available from a new "Claude Skill" tab on the Deployment page. Destructive actions require in-chat confirmation.
- New `GET /api/vms/{uuid}` endpoint returns a single VM by UUID across all hosts.

### Fixed
- "Update N selected" on the kvm-tools page now succeeds for Windows guests; the server serves the correct Windows binary and the self-update no longer fails with the file in use. Windows guests on an older daemon need one more manual update to pick up the fix.

## v5.32

### Added
- Live migration progress now streams in the UI while the migration is in flight, instead of jumping from 0 to 100% on completion.
- Admin can rotate the migration grant-signer key on demand, with a 10-minute grace window so in-flight grants keep working.
- Admin can trigger an on-demand inventory reconcile for a host and see a diff of added, removed, and changed VMs; unreachable hosts report clearly instead of timing out.
- Control-plane backup now bundles TLS material, the cluster CA, and the grant signer alongside the store, so a single tarball can rebuild a working deployment; a paired restore flow verifies and unpacks it.
- Certificate-expiry warnings now cover the cluster CA and every enrolled hypervisor certificate, not just the server's own, with opt-in notification channels for each.
- Admin can force-fail a migration stuck in cleanup, and an admin command log records every action with a request ID for tracking.

### Changed
- All control-plane traffic (UI, agents, in-guest tools, CLI) now runs over TLS only; the plain-HTTP listener has been retired.
- Commands carry a request ID and are idempotent, so a dropped response and client retry no longer produce a duplicate snapshot, migration, or edit.
- Stale phantom hosts (offline 30+ days) are pruned automatically.

### Fixed
- Stopped VMs no longer report phantom CPU, memory, or network usage; they correctly show zero.
- Live migrations can no longer be silently killed by an agent-update pass while in flight.

### Removed
- The plain-HTTP `:8090` listener has been removed; the server binds the TLS listener only.

## v5.31

### Added
- A cap-raise helper offers to shut down, raise the vCPU/memory cap, and restart a VM in one click when libvirt refuses a live change.

### Changed
- The per-host VM list gains sortable columns, a name-confirmation prompt before snapshotting, and a "migrating" indicator that locks rows mid-migration.

### Fixed
- VM memory readings refresh every few seconds and no longer report stale or double-counted "used memory".

## v5.30

### Added
- New "Power cycle" action does a full graceful shutdown and start so pending CPU/memory cap changes actually take effect; the plain in-guest restart is unchanged.

### Changed
- Agents refuse to self-update while a migration is in flight, preventing orphaned transfers.

## v5.29

### Changed
- Offline migrations now stream live phase and progress to the UI without polling.
- VM state changes appear in the UI within a couple of seconds instead of up to fifteen.

### Fixed
- Migrations stranded by a server restart are reconciled automatically instead of showing "running" indefinitely.

## v5.28

### Added
- Running VMs report live CPU and memory in near real time rather than on the slower telemetry tick.

## v5.27

### Added
- Live hardware edits: change a running VM's vCPU and memory, applied live where libvirt allows and on next reboot otherwise.
- CD/DVD media swap: eject and insert ISOs against a VM's optical drive without a reboot.
- Admin backup export streams a full persistence snapshot as a `tar.gz` that doubles as a restore source.

### Changed
- Promoting a build to production is now an explicit operator action rather than an automatic redeploy.

## v5.26

### Added
- Remove a hypervisor from inventory directly from Host detail, clearing its VM, storage, metrics, and enrolment records without touching the physical machine; blocked while a migration references the host.
- VMs that cannot take internal snapshots now show a clear "Snapshots unsupported" badge with the reason and a fix.

### Changed
- The agent install one-liner is fully self-contained and works piped through `curl | bash`.

## v5.25

### Added
- A "Fit to window" control in the VNC console and a viewport that no longer clips tall guest displays.

### Changed
- The built-in Operator role now covers everyday VM create, delete, migrate, and storage cleanup; platform-admin permissions stay Administrator-only.
- The bundled Grafana dashboard is redesigned into a single dense, import-friendly layout with fleet, per-host, and per-VM views.
- The interactive installer prompts for the HTTPS port and discovers the LAN IP across Linux and macOS.
- The complete RBAC permission reference is now documented.

### Fixed
- The Windows kvm-tools install one-liner now works end-to-end against a stock Windows guest.
- Offline migration warns when a VM has snapshots (which do not transfer); live migration still blocks on them.

## v5.24

### Changed
- TLS is the default everywhere: agents, kvm-tools, and install snippets target `https://<server>:8190`. The plain-HTTP listener remains for backwards compatibility but is no longer advertised, and a warning persists until a trusted certificate is installed.

## v5.23

### Added
- A "Public URL" setting threads the canonical server address into every install snippet and notification deep link.

### Changed
- Shipped scripts and binaries no longer carry environment-specific defaults, using neutral placeholders instead.

## v5.20

### Added
- Interactive installer wizard prompts for database credentials and cluster domain, then optionally deploys; it detects an existing install and offers keep, reconfigure, fresh, or cancel.
- First-boot web wizard walks operators through hypervisor enrolment, notifications, and metrics, with a skip option at every step.

### Changed
- The Deployment page splits downloads into per-component tabs (KVM Tools, Hypervisor Agent, kvmctl CLI).

### Fixed
- Saving settings no longer re-triggers the first-boot wizard from a stale browser tab.

## v5.19

### Removed
- The legacy v3 HTML UI and its separate container are gone; the React app has been the default since v4.0.0. Old `/legacy` and `/v4` links redirect to it.

## v5.18

### Added
- Every heavy list (Hosts, VMs, Snapshots, Storage, Tools, Snapshot Policies) now has a scrollable region with a sticky header and a pagination footer (10/25/50/100/All).

### Changed
- Dropdowns and scrollbars are themed dark consistently across the app instead of showing native OS chrome.

### Security
- The "disable console" setting now blocks the entire console surface, closing a deep-link bypass.
- The "disable snapshots" setting now also halts scheduled snapshot policies and blocks policy writes.

## v5.17

### Added
- Host detail shows last-hour CPU, memory, and storage sparklines.
- The VMs list and Host detail show a guest-tools status pill (current / outdated / missing / none).

### Changed
- Host detail no longer duplicates the VM list across tabs; it lives only on the richer VMs tab.

## v5.16

### Changed
- The standalone build portal is retired; agent, kvm-tools, and kvmctl binaries plus install scripts are now served by the server under `/downloads/*`.

### Removed
- The `:8097` build-portal endpoint. Old `:8097` bookmarks no longer resolve; installers and update scripts default to the new path automatically.

## v5.15

### Added
- New Operations documentation covering networking and firewall ports, migration bandwidth budgets, and remote-access topologies.

## v5.14

### Added
- In-app documentation site served at `/docs`, with a grouped sidebar and a Help icon in the top bar.

## v5.13

### Added
- InfluxDB v2 metrics sink with a bundled Grafana dashboard template; host and per-VM samples ship on every telemetry tick, with a Metrics settings tab and test-sample button.

## v5.11

### Added
- Signed, single-use migration grants bind each transfer to source, destination, VM, and a short expiry, with audit events for issue, accept, and reject.
- Offline migration over `qemu+tls` streams disk data through the libvirt connection, removing the need for SSH keys on the migration path.
- Advanced offline-migration options: choose the destination storage pool, rename the VM (UUID preserved), and regenerate MAC addresses.
- A transport toggle (Auto / mTLS / SSH) in the migrate dialog, with mTLS disabled when either host is unenrolled.

### Changed
- The target-host picker is sorted and filterable so it no longer reshuffles on telemetry refresh.

### Fixed
- Offline migration succeeds against hosts without a "default" pool and no longer fails when overwriting an existing disk.
- The migration record shows the actual transport used instead of always reading "SSH".

## v5.10

### Added
- Cluster CA and hypervisor identity PKI: a self-generated cluster CA, one-time enrolment tokens, and per-host leaf certificates, managed under Settings → TLS.
- Migrations use a `qemu+tls` channel when both hosts are enrolled, falling back to `qemu+ssh` for mixed fleets.

### Changed
- `install-agent.sh` configures libvirt TLS in one shot, including socket activation and firewall rules.

### Fixed
- Hypervisor certificates include both IP and FQDN SANs so TLS migration dials succeed.
- The mTLS status badge now renders correctly, and live migration no longer leaves a ghost domain on the source.

## v5.7

### Added
- Per-channel notification routing so each channel can override the global event subscription.

## v5.6

### Added
- Personal API tokens any signed-in user can mint from the user menu (30-day default), alongside admin-only system tokens.

### Fixed
- No-expiry tokens no longer display as "EXPIRED".

## v5.5

### Added
- API tokens and the `kvmctl` CLI: bearer credentials shown once, with commands for hosts, VMs, lifecycle, migrate, and snapshots.
- `kvmctl whoami`, `config`, and `logout`, a one-shot installer that detects OS and architecture, and a "Via" column in the audit log.

## v5.4

### Added
- Notifications via webhook (Discord, Slack, generic) and email, triggered by VM crash/recovery, host offline/recovery, migration complete/failed, expiring TLS certs, and failed snapshot policies, with rich severity-coded embeds.
- Drain-VMs-on-maintenance flow with a confirmation dialog.

### Fixed
- The "migration complete" notification now fires reliably.

## v5.3

### Added
- Scheduled snapshot policies with retention; policy-tagged names leave manual snapshots untouched.

### Changed
- Snapshot policies moved into a dedicated tab under Snapshots.

## v5.2

### Added
- Host maintenance mode that refuses new VM placements and migration targets.
- Audit shipping to an external syslog or HTTP aggregator.

## v5.1

### Added
- MongoDB backend with SCRAM authentication; existing JSON stores migrate transparently on first boot.

## v5.0

### Added
- Server-proxied VM console so console sessions are tied to the signed-in session.

## v4.0 and earlier

### Added
- Initial single-page web UI at `/` covering hosts, VMs, snapshots, migrations, storage, and a deployment page, completing the port from the legacy v3 interface.
