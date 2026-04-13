# Changelog

## v4.4.0 — v4 wrap-up (2026-04-13)
- Multi-step **VM delete** now has an explicit "Also delete the backing disk
  files" checkbox (default off). Agent honors `delete_disks=true|false` and
  logs when disks are intentionally kept.
- **VMDetail Actions menu** is now locked when the VM has a running migration
  on the source host, or a failed migration whose phase reached `define`+ on
  the destination. A "migrating" / "rolled back" pill renders next to the
  state pill.
- **Storage page** paginates at 15 pools/page, matching VMs / Hosts / Migrations.
- Pagination is now a **shared component** with proper light-mode styling.
- New **lock-pill** and **topbar-mig-badge** CSS classes with light-mode
  variants — replaces the inline dark-only swatches.
- VMDetail summary shows a "**KVM TOOLS: not installed**" pill (greyed) when
  the guest agent isn't reporting, instead of nothing at all.
- **CHANGELOG.md** added.

## v4.3.1 (2026-04-13)
- VM and Host list rows are fully clickable (whole `<tr>` navigates to detail).

## v4.3.0 (2026-04-13)
- Migrations page: free-text search across VM / source / destination,
  Status + Mode dropdown filters, Clear button, paginated history at 15/page.
  Active migrations are pinned above the filters and never hidden.

## v4.2.3 (2026-04-13)
- MigrateModal "Run in background" / backdrop / Esc all redirect to
  `/migrations` so the operator can't accidentally fire follow-ups against
  the in-flight VM.

## v4.2.2 (2026-04-13)
- VMs list locks the source row while a migration is running and (if the
  rollback may have left dest artifacts) the destination row after a
  failed migration.

## v4.2.1 (2026-04-13)
- Server rejects MIGRATE / OFFLINE_MIGRATE with 409 when another migration
  is in flight on the same source or destination. MigrateModal mirrors the
  gate client-side with a "Host busy" banner.

## v4.2.0 (2026-04-13)
- Server auto-creates a `MigrationRecord` on every MIGRATE / OFFLINE_MIGRATE
  and (for offline) polls the agent's `/migrate-status` until terminal —
  fixes "migration not in summary page".
- New **MigrationCenter** modal listing all in-flight migrations, reachable
  from a pulsing topbar badge that appears whenever active > 0.
- MigrateModal stays open after submit with a live progress card; "Run in
  background" minimises while the migration continues.
- Heading polish: drop trailing `(N)` counts on VM/Host detail and
  Snapshots overview headings.

## v4.1.3 (2026-04-13)
- `server/VERSION` file is now the single source of truth (embedded into
  the binary via `go:embed`, also stamped into the build-portal page).
- Dashboard sorts hypervisors by id (stable across SSE ticks) and
  paginates at 10/page.

## v4.1.2 (2026-04-13)
- Drop the `-X main.Version=...` Docker override so the committed const
  isn't clobbered with `dev`.

## v4.1.1 (2026-04-13)
- Dashboard KPI cards: replace `auto-fit` grid with `repeat(4, minmax(0,
  1fr))` so all four cards stay equal width.
- Bump committed Version const.

## v4.1.0 (2026-04-13)
- Sortable headers on Hosts / VMs / Storage (new `SortableTH` + `useSort`).
- Editable VM notes inline editor wired to `POST /api/vms/notes`
  (gated on `vm.edit`).
- VMDetail Summary: right columns aligned across rows, Launch Console
  bottom-aligns with the info panel.
- Snapshot Revert/Delete buttons use inline SVG icons.

## v4.0.0 — v4 cutover (2026-04-12)
- React SPA mounted at `/`, legacy SPA preserved at `/legacy/`.
- Full v3 → v4 port: Dashboard, Hosts, VMs, Storage, Snapshots, Migrations,
  Settings (General/Users/Groups/Roles/Providers), Deployment.
- Self-service topbar: user menu, logout, theme toggle, 2FA enrolment.
- Create VM wizard.
- Hypervisor Summary with meta row, big-stat cards, utilisation bars, VM
  table.
- Remote file browser inside the Storage view.
