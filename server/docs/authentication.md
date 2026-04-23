---
title: Authentication
section: Reference
order: 40
---

# Authentication

kvm-center supports two primary authentication mechanisms, both
homed against the same user + RBAC store.

## Browser sessions

The SPA uses cookie-backed sessions. Login flows:

- **Local password** — username + password against the built-in
  user store. TOTP (RFC 6238) second factor available per-user.
- **External IdP** — OIDC (generic + Google + GitHub + Microsoft
  presets), SAML 2.0, LDAP/AD. First-time IdP login creates a user
  with the IdP's default role; subsequent logins match by
  `(provider_id, subject)`.

Session cookies are `HttpOnly`, `SameSite=Strict`, scoped to `/`.
Expiry defaults to 24 h and slides on activity. The store prunes
expired sessions every 10 minutes.

## API tokens

For scripting (`kvmctl`, CI, Ansible, etc.) mint a bearer token.
Two tiers:

| Kind | Who | Default expiry | Managed at |
|------|-----|----------------|------------|
| **User** | Any signed-in user, for themselves | 30 days | User menu → My API tokens |
| **System** | Admins only, for service accounts | No expiry | Settings → API Tokens |

Both inherit the owner's permissions. Wire format:

```
Authorization: Bearer kvmc_<id>.<secret>
```

The secret is shown **once** at creation and persisted as SHA-256
only. Lose it, mint a new one.

### Audit attribution

Every action taken via a token records `detail.via=token:<id>` on
the audit event. The Audit Log's **Via** column distinguishes:

- **CLI · <token-id>** — a bearer token
- **WebUI** — a browser session
- **System** — background tasks (snapshot scheduler, drain,
  migration poller)

Filtering the audit log by action + outcome, then scanning the Via
column:

![Audit log filter and inspect](/api/docs/media/06-audit-log.webp)

## RBAC

Every API endpoint is gated on a permission string. Permissions are
enforced by the server — the SPA just uses them to grey out buttons.

### Permission reference

| Permission | Grants |
|------------|--------|
| `admin` | Wildcard — implies every other permission, including ones added in future releases. Use sparingly. |
| `vm.view` | Read the VM inventory, per-VM detail pages, and telemetry. |
| `vm.lifecycle` | Start / stop / reboot / force-off a running VM. |
| `vm.create` | Provision new VMs through the Create VM wizard. |
| `vm.delete` | Undefine (permanently destroy) a VM domain and its disks. |
| `vm.migrate` | Live or offline-migrate a VM between hypervisors. |
| `vm.snapshot` | Create, revert, and delete libvirt domain snapshots + manage snapshot policies. |
| `vm.console` | Open the VNC console and view per-VM screenshots / thumbnails. |
| `vm.edit` | Edit VM notes and hardware (CPU / RAM / NICs). |
| `host.view` | Read the hypervisor inventory and per-host detail pages. |
| `host.manage` | Toggle maintenance mode, re-enrol, or remove a hypervisor. |
| `storage.view` | Browse storage pools and the per-pool file list. |
| `storage.upload` | Upload ISOs and disk images into a pool. |
| `storage.delete` | Delete files from a pool through the UI. |
| `user.manage` | Create / update / disable users, groups, and custom roles. |
| `tls.manage` | Upload / regenerate / download the server TLS cert and the cluster CA material. |
| `audit.view` | Read the audit log (Settings → Audit). |

### Built-in roles

| Role | Permissions |
|------|-------------|
| **Administrator** | `admin` (wildcard) |
| **Operator** | `vm.view`, `vm.lifecycle`, `vm.create`, `vm.delete`, `vm.migrate`, `vm.snapshot`, `vm.console`, `vm.edit`, `host.view`, `storage.view`, `storage.upload`, `storage.delete` |
| **Viewer** | `vm.view`, `host.view`, `storage.view` |

Built-in roles are managed — they can't be deleted or edited. The
Operator role covers every VM and storage action a day-to-day
operator needs; platform-admin permissions (`host.manage`,
`user.manage`, `tls.manage`, `audit.view`) intentionally stay on
the Administrator role only.

Custom roles: Settings → Roles → **New role** — pick any subset of
the permissions above and assign to users or groups.

## VM console access

Launching the VNC console is the most sensitive day-to-day
permission — an operator with `vm.console` can read the screen,
send keystrokes, and move the mouse on a running guest. It is
protected by a **defence-in-depth stack**, not a single gate.

### Five layers of control

1. **Authentication.** The console HTML page (`/api/console/<host>/<vm>`)
   and the WebSocket upgrade (`/api/console-ws/<host>/<vm>`) both
   sit behind the same session middleware as the rest of the API.
   No cookie / no API token → 401. Sharing a console URL with
   someone who isn't signed in grants them nothing.
2. **RBAC.** Both endpoints are wrapped in `requirePerm("vm.console")`.
   Built-in Operator and Administrator roles include it; Viewer
   does not. Custom roles can grant or withhold it independently.
3. **Feature flag.** Settings → General → **Remote console** toggles
   `disable_console`. When off, the HTML page, the WebSocket
   upgrade, the `/api/vms/console-token` mint, and the per-VM
   screenshot endpoint all return 403 with a clear error. A user
   carrying `vm.console` can't deep-link past it.
4. **HMAC-signed short-lived tokens.** Before every WebSocket
   splice the server mints a 5-minute HMAC token bound to
   `{host, vm, exp, nonce}`. The agent re-validates the signature
   before it upgrades the socket to VNC. Rationale: pre-v5.0.2 the
   agent served `/console/<vm>` and `/ws/<vm>` with no auth —
   anyone on the management network who could type a URL had full
   console access. The HMAC plugs that hole even if an agent is
   accidentally exposed beyond the control plane's subnet.
5. **Audit trail.** Every console open writes a `vm.console` audit
   event with the acting user, VM name, and host ID. The
   WebSocket upgrade that follows isn't double-counted — one click,
   one event. Admins can review at **Settings → Audit** or ship
   the stream to an external aggregator via Settings → Audit
   shipping.

### Networking posture

The WebSocket is proxied server-side; the agent's VNC-proxy port
(default `:8081`) **never needs to be exposed to the browser**.
All console traffic rides the HTTPS channel on `:8190` along with
the session cookie. Firewall `:8081` down to the control plane's
subnet; browsers stay on `:8190`.

### Recommendations for high-trust environments

- Require **TOTP (2FA)** on accounts holding `vm.console` — the
  user menu shows a per-account toggle.
- Don't bake `vm.console` into a shared "everyone" role. Use the
  Operator role for VM custodians only; create a read-only role
  without it for auditors.
- Install an **operator-provided TLS cert** so the session cookie
  isn't tunnelled through a self-signed warning dismissed out of
  habit. See Settings → TLS.
- Rotate operator sessions on a schedule (Settings → Sessions) —
  a long-lived cookie on a lost laptop is the highest-impact
  credential exposure console access implies.

## TOTP (2FA)

User menu → Security & 2FA → Start setup. A QR code + manual-entry
key appear; scan with any RFC 6238 authenticator. Enter a
verification code to enable. Login subsequently requires the
code after the password succeeds.

## Examples

### curl with a token

```sh
# Bearer token format: kvmc_<8-char id>.<40-char secret> — paste the real
# value from Settings -> API Tokens.
curl -H "Authorization: Bearer kvmc_YOUR_TOKEN_HERE" \
     https://kvm.example.com:8190/api/hosts
```

### kvmctl login

```sh
kvmctl login --server https://kvm.example.com:8190 --token $TOKEN
kvmctl whoami
```
