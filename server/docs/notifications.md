---
title: Notifications
section: Operations
order: 65
---

# Notifications

kvm-center fans operator-relevant events out to one or more sinks. A
**channel** is a delivery target; a **subscription** picks the event
types that get routed to a channel. Channels and subscriptions live
under **Settings → Notifications**; nothing is enabled by default.

## What fires a notification

Triggers are baked into the server — operators choose which to act
on, not whether they fire. The current set:

| Event type                   | Fires when |
|------------------------------|------------|
| `vm.crashed`                 | Telemetry transitions a VM into crashed/shutoff. |
| `vm.recovered`               | A previously-crashed VM transitions back to running. |
| `host.offline`               | A host stops sending telemetry past the freshness window. |
| `host.recovered`             | The host comes back online. |
| `migration.complete`         | A live or offline migration finalises successfully. |
| `migration.failed`           | A migration record terminates with status=failed. |
| `tls.expiring`               | Server TLS cert expires within the warning window. |
| `ca.expiring`                | Cluster CA cert expires within the warning window. |
| `hypervisor.cert.expiring`   | A leaf hypervisor cert expires within the warning window. |
| `snapshot.policy.failed`     | A scheduled snapshot policy returns an error from the agent. |

The full list (including any added in newer releases) is exposed in
the UI's "Default event subscriptions" panel.

## Channels

Three kinds, configured per-channel:

### Webhook

A POST to a URL with one of three payload shapes — auto-detected
by hostname, or pinned via the *Payload format* selector:

- **Discord** — rich embed with severity-coloured left bar and
  inline fields. Auto-selected when the URL host contains
  `discord`.
- **Slack** — `attachments` envelope with the same colour + field
  treatment. Auto-selected when the URL host contains `slack`.
- **Generic** — raw event JSON. Useful for n8n / Zapier / any
  custom listener.

Optional **HMAC secret**: if set, kvm-center signs the body with
HMAC-SHA256 and ships the digest in the `X-KVMCenter-Signature`
header. Recipients can verify integrity + origin.

### Email

SMTP delivery. Per-channel `to` + `from` overrides; falls back to
the global SMTP defaults (host, port, user, password, STARTTLS) if
left blank.

### Discord Gateway *(v5.52.0+)*

Routes notifications through a paired **Discord Gateway**
companion service — a fan-in proxy that owns the persistent
websocket to Discord and lets multiple sources (kvm-center +
alertmanager + grafana + ad-hoc JSON) share a single audit
channel without each holding its own bot token. Pairing
exchanges credentials programmatically; there's no token to
copy or webhook URL to invent.

In **Settings → Notifications**, configure the gateway in the
**Discord Gateway** panel (toggle on, enter host + port, save,
approve in the gateway's UI), then add a `+ Add Discord Gateway
channel` from the channels list above to pick which event types
fan into the audit channel.

Full operator reference — pairing protocol, state machine, TLS
posture, security model, troubleshooting — lives in the
dedicated [Discord Gateway](/docs/discord-gateway) page.

## Per-channel subscriptions

By default, every channel inherits the global *Default event
subscriptions* set (the checkboxes at the top of the
Notifications tab). Override per-channel by ticking *Override
defaults* on the channel and choosing the events.

Use case: route `migration.failed` to a PagerDuty webhook and the
chatty `vm.crashed` stream to a quieter Discord channel.
