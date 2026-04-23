---
title: Walkthroughs
section: Getting started
order: 25
---

# Walkthroughs

Short recordings of common operator flows, captured against a
representative demo deployment. Each clip is a silent WebP that
loops in the browser — no scrubber, no controls, just the flow.

## Sign in and tour the dashboard

Landing on the login screen, signing in, and scanning the
hypervisor cards on the dashboard.

![Sign in and tour the dashboard](/api/docs/media/01-login-dashboard.webp)

## VM lifecycle: shutdown, start, snapshot

Opening a running VM, shutting it down, starting it again, and
taking a snapshot — all from the Actions menu on the detail view.

![VM lifecycle](/api/docs/media/02-vm-lifecycle.webp)

## Offline migration with progress

Migrating a VM to another host using the Offline mode so the
progress arc is visible. Live migrations return synchronously and
don't render a progress bar.

![Offline migration](/api/docs/media/03-live-migration.webp)

---

These clips are generated from the demo stack via
`scripts/record-walkthroughs.sh`. The Playwright specs live under
`demo/walkthroughs/specs/` and are Gitea-only (scrubbed from the
public GitHub mirror).
