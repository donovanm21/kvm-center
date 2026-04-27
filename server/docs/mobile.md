---
title: Mobile WebUI
section: Operations
order: 67
---

# Mobile WebUI

A read-only iPhone-first SPA at `/m`, shipped in v5.46.0. It
shares the same API, auth, and React Query cache as the desktop
SPA, so flipping between the two never refetches.

## When to use it

- Glance-at-it ops on a phone — fleet overview, host status,
  VM state, recent activity.
- Walking the rack: a quick check that everything's still up.
- Any time you don't want to log into the full desktop SPA on a
  pocket-sized screen.

It's deliberately read-only. Snapshot create, migrate, edit-VM,
restart-VM, install-tools — anything that mutates state — stays
on the desktop SPA.

## How operators land on it

- **Auto-redirect from `/`.** Any iPhone, iPod, or Android-mobile
  user-agent (or a viewport ≤ 640 px) hitting the root URL is
  bounced to `/m` automatically. Deep links (e.g. `/vms/<uuid>`)
  are honoured — no redirect.
- **Add to home screen.** [manifest.json](/manifest.json) declares
  `start_url: /m`, so the installed PWA launches straight into
  the mobile shell with no Safari chrome.
- **Direct URL.** `https://<server>:8190/m` works for any client.

## Screens

| Tab       | What it shows |
|-----------|---------------|
| Overview  | Hosts-online tile, VMs-running tile, active migrations tile, total VMs tile, recent migrations card list |
| Hosts     | Card per host: id, IP, VM count, status pill, CPU + memory meters. Filter by id/IP. |
| Hosts → detail | OS, kernel, libvirt version, CPU model, cores, memory, uptime, on-host VM list (read-only) |
| VMs       | Card per VM: name, host, vCPU, memory, state pill. Filter by name/host/hostname. State chips for `all` / `running` / `stopped`. |
| VMs → detail | Resources (vCPU, memory, disk, snapshot count), guest agent block (OS, hostname, kernel, uptime, IPs, kvm-tools version), per-filesystem usage meters |
| Activity  | Latest 50 audit events, glance-at-it stream, no filters (use the desktop Settings → Audit tab for filtered drill-in) |

The bottom tab bar is fixed-position, so it stays visible at all
times regardless of how long the page is. List ordering is stable
across React Query polls (10 s) — hosts sort by id, VMs by name —
so cards don't shuffle on tick.

## Going back to desktop

There's no in-UI escape hatch — phones get the mobile experience.
If you genuinely need the desktop SPA on a phone-class device,
set `localStorage.kvmc-prefer-desktop = '1'` from dev tools and
reload; the auto-redirect will stop firing for that browser.

## Network expectations

The mobile shell is just routes inside the same SPA bundle, so
there's no separate deployment, no separate build artefact, and
no extra port to expose. The PWA install requires HTTPS — the
self-signed cert that ships with kvm-center works once the
browser has accepted it.
