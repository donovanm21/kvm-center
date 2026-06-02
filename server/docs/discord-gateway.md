---
title: Discord Gateway
section: Operations
order: 66
---

# Discord Gateway

Operator guide for routing kvm-center audit events, alerts, and
notifications into a Discord channel via a paired **Discord Gateway**
companion service. This page is the kvm-center-side reference —
what *you* do in this UI. The gateway's own setup (running the
container, OAuth, channel/role bootstrap) lives in the gateway
repo's setup guide.

The gateway is **optional**. If you're not using it, leave the
panel disabled — kvm-center keeps the audit log locally either way.

## What it gives you

Once paired, every notification kvm-center fires lands as a
formatted Discord embed with consistent styling:

- **Coloured left bar** for severity (info / success / warn /
  critical)
- **Title** — short event description ("Snapshot created")
- **Body** — long-form context, Markdown-formatted
- **Footer chips** — event type and resource
  (`vm.snapshot.create`, `vm/<uuid>`)
- **Inline fields** — supplementary metadata (size, pool, actor)
- **Timestamp** + `kvm-center vX.Y.Z · <deployment>` footer

The same gateway also runs `/kvmc status`, `/kvmc snapshot`, and
the rest of the slash-command surface, plus a bug-report relocator
that turns public posts in `#report-a-bug` into private threads
visible only to the reporter and your maintainer role. Those
features are gateway-side concerns and not configured from
kvm-center.

## How it fits

```
kvm-center                       Discord Gateway          Discord
──────────                       ───────────────          ───────
fireNotification ──┐
                   ▼
              ┌───────────┐   POST /pair/request   ┌──────────────┐
              │  paired   │ ──────────────────────▶│  pairings    │
              │  gateway  │   GET  /pair/status    │  manager     │
              │  client   │ ◀──────────────────────│              │
              └─────┬─────┘                        └──────┬───────┘
                    │       POST /webhook/<id>            │
                    │       Bearer <token>                │
                    └────────────────────────────────────▶│
                                                          ▼
                                                    embed render
                                                          │
                                                          ▼
                                                       channels
```

Three workers run inside kvm-center while the gateway is enabled:

| Worker          | Cadence | What it does |
|-----------------|---------|--------------|
| Pairing poller  | 5 s for 2 min, then 30 s | Polls `/pair/status` while *Pending*; gives up after 1 h. |
| Health monitor  | 30 s    | Polls `/healthz`; two consecutive failures flip to *Unreachable*. |
| Send queue      | drains as fast as the gateway accepts | Bounded at 1024 events; oldest dropped on overflow. |

## Setup, in this UI

You'll need a running gateway before starting — see the gateway
repo's setup guide for that side. With the gateway up:

1. **Settings → Notifications → Discord Gateway**.
2. Toggle **Enabled** on.
3. Enter the gateway's **host** (IP or FQDN) and **port** (default
   `8090`).
4. Decide on **TLS verification**:
   - The gateway ships with a **self-signed cert** out of the box.
     Leave **Skip TLS verification** *checked* for that case.
   - If you've added the gateway's CA to this server's trust store
     (or fronted the gateway with a public cert), uncheck *Skip
     TLS verification* for proper certificate validation.
5. Click **Save**. Status flips to *Saving* → *Pending*.
6. **Approve in the gateway UI.** Open
   `https://<gateway-host>:<port>` → **Pairings** tab → **Approve**
   the request from your kvm-center deployment.
7. kvm-center's badge flips to *Healthy* within ~30 s.
8. Add a **`+ Add Discord Gateway channel`** entry on the channels
   list above the panel and tick the event types that should fan
   into the audit channel.

The button is disabled until the gateway is enabled — you can't
create a no-op channel that ships into nothing.

## State badge

The badge surfaces nine states; tooltip on the badge shows the
last health timestamp + error.

| Status        | Meaning |
|---------------|---------|
| `disabled`    | Toggle off — no traffic, no health polling. |
| `saving`      | `/pair/request` in flight. |
| `pending`     | Registration filed; waiting for operator approval in the gateway UI. Will time out after 1 h. |
| `approved`    | Token captured; first `/healthz` response not yet returned. |
| `connecting`  | Gateway up, Discord websocket not yet ready (gateway just started, or Discord-side outage). |
| `healthy`     | Gateway up + Discord ws ready; events flowing. |
| `unreachable` | Two consecutive `/healthz` failures. Auto-recovers on the next success. |
| `revoked`     | Gateway returned 401/404 on a webhook send — operator must re-pair. |
| `timeout`     | Pending > 1 h with no approval. Re-toggle to retry. |

## Buttons

| Button | What it does |
|--------|--------------|
| **Save** | Persist current form values; if Enabled flipped on (or host/port/TLS changed), kicks a fresh `/pair/request`. |
| **Re-pair** | Saves form first, then forces a clean re-pair: clears the registration ID + token + status, fires `/pair/request` again. Use after the operator deletes the pairing on the gateway side. |
| **Send test** | Ships a synthetic `audit.gateway.test` card through the live wiring. Disabled while status is anything other than *healthy*. |
| **Revoke** | Wipes local creds + drops to *disabled*. Doesn't reach back to the gateway — also revoke there for a complete teardown. |

When the panel detects an `x509:` / "tls: failed to verify
certificate" error in the gateway's last response and TLS
verification is currently **on**, an inline banner offers a
one-click **Disable TLS verification + re-pair** auto-fix.

## What gets routed where

There are **two independent streams** that can flow through the
gateway:

1. **Notification events** — the curated set (vm.crashed,
   host.offline, migration.failed, snapshot.policy.failed, etc.).
   Routed via a `discord-gateway` channel in the channels list
   above the panel; honours the per-channel + global event-type
   subscriptions.
2. **Audit log firehose** *(v5.53.0+)* — every entry that lands
   in the audit log (`/api/audit`). Off by default; flip the
   **Ship audit log entries to Discord** toggle in the panel
   to enable, and choose whether to ship only **failed events**
   (recommended — quieter) or every entry.

Both streams use the same paired connection and the same send
queue. Both honour the gateway's drop-on-overflow behaviour.

By default, every event ticked in **Default event subscriptions**
(or the per-channel override on the `discord-gateway` channel)
fans into a single Discord channel — the **audit channel** the
gateway operator configured during gateway-side bootstrap. If
that's empty, kvm-center will get `424 Failed Dependency` on
the first send and the panel will surface
"gateway has no audit channel — run Bootstrap" in the
*Last send error*.

If you want different events going to different channels, either:

- Run the gateway's **Webhooks** tab to create per-purpose
  webhook instances and route them to different channels, or
- Add multiple `discord-gateway` channels in kvm-center with
  different event-type subscriptions (each goes through the
  same gateway pairing — channel routing is a gateway-side
  concern).

## Send queue + drops

Bounded in-memory queue, cap **1024 events**. Sustained outage
beyond that drops *oldest* and increments `dropped_events` —
the panel surfaces the running count so you can spot when
notifications are being lost.

Discord notifications are advisory, not durable. Lost events
during a gateway outage are recoverable from kvm-center's audit
log (`/api/audit`) on demand — the gateway is a courier, not a
source of truth.

## Security posture

### Permissions

The gateway settings endpoints are gated on the **`admin`**
wildcard — stricter than the rest of Settings → Notifications,
which allow `user.manage`. Rationale: a paired gateway carries
a bearer-credential-grade token and can quietly redirect every
notification kvm-center fires to a third-party service. Keeping
that change off custom roles by default is the right tradeoff.

### Token storage

Bearer tokens land in the `discord_gateway` mongo singleton
**plaintext** — same pattern as SMTP password and Influx token.
The API surface redacts the value to `(configured)` before
returning it; logs never include it. Encryption-at-rest is
tracked separately for the secrets-table work that will rotate
all three together.

### TLS posture warning

When *Skip TLS verification* is on, kvm-center logs a warning
every 6 h to keep the choice visible:

```
discord-gateway: verify_tls is disabled — gateway cert is not
validated. Acceptable on a private LAN; not for public-facing
gateways. (host=…)
```

Acceptable on a private LAN where you control the gateway's
hostname; not acceptable on a public-facing gateway.

### Audit attribution

Every operator action against the gateway records a dedicated
`discord.gateway.*` audit event so post-incident review can
distinguish gateway operations from audit-shipping or notification
edits:

| Action                     | Fires on |
|----------------------------|----------|
| `discord.gateway.update`   | PUT `/api/settings/discord-gateway` (config save). |
| `discord.gateway.pair`     | POST `/api/settings/discord-gateway/pair` (re-pair). |
| `discord.gateway.revoke`   | POST `/api/settings/discord-gateway/revoke`. |
| `discord.gateway.test`     | POST `/api/settings/discord-gateway/test` (test send). |

## Day-2 operations

### Pausing

Toggle the panel off. The pairing stays approved on the gateway
side; flipping back on resumes immediately, no re-pair needed.

### Replacing the gateway host

Update **host** and **port** in the panel and click **Save**.
kvm-center detects the endpoint change and fires a fresh
`/pair/request` — the previous pairing is left orphaned on the
old host (clean up there if it's still reachable).

### Revoking + re-pairing

If a token rotates on the gateway side (or the operator
explicitly revokes the pairing there), the next webhook send
gets a 401 / 404 and kvm-center flips to *revoked*. From the
panel, click **Re-pair** and approve again on the gateway side.

### Removing the integration

In kvm-center: click **Revoke**, or just toggle off. Notifications
stop. Local creds are wiped. To fully unwind, also revoke the
pairing on the gateway side (its **Pairings → Revoke**).

## Troubleshooting

### Status stays *pending*

The gateway received the pair request but no operator has
approved it yet. Open the gateway's web UI; there's a `1` badge
on the **Pairings** tab. Click *Approve*.

If it stays pending past an hour, the panel flips to *timeout*.
Toggle off then on again to retry.

### Status flips to *unreachable*

- **Gateway down or unreachable from this server.** Confirm the
  gateway is actually running (`docker ps` on the gateway host)
  and the port is open from kvm-center's network position.
- **TLS error.** If the gateway error in the panel mentions
  `x509:` / "tls: failed to verify", the auto-fix banner will
  surface — one click disables verification and re-pairs.
- **Wrong port / host.** kvm-center's last health error includes
  the URL it tried; sanity-check the form against the gateway's
  actual listener.

### Status flips to *revoked* unexpectedly

The gateway returned 401 or 404 on a webhook send. Either the
operator revoked the pairing on the gateway side, the bearer
token was rotated, or the webhook instance was deleted. Click
**Re-pair**, approve again on the gateway side.

### Status is *healthy* but no cards arrive

- **No event types subscribed.** Check the `discord-gateway`
  channel above the panel has at least one event type ticked.
- **Audit channel not configured on the gateway side.** The
  panel's *Last send error* will say "gateway has no audit
  channel — run Bootstrap"; that's a gateway-side fix, not a
  kvm-center one.
- **Bot lacks `SEND_MESSAGES` / `EMBED_LINKS` on the audit
  channel.** Gateway-side problem; the gateway's Bootstrap tab
  has a permission audit that flags this.

### Test send works but real events don't

The `Send test` button bypasses the notifications dispatcher —
it ships a synthetic card straight through the gateway client.
If test works but real events don't, the issue is upstream of
the gateway:

- The triggering event type isn't in the channel's subscription.
- The trigger itself isn't firing (e.g. the relevant background
  monitor is disabled).

Check the **Audit Log** for matching events — every operator
mutation appears there regardless of notification config, so
you can confirm the trigger fired before debugging routing.
