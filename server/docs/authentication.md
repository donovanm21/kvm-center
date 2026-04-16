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
