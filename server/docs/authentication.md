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

Every API endpoint is gated on a permission string. Built-in roles:

| Role | Permissions |
|------|-------------|
| Administrator | `admin` (wildcard) |
| Operator | `vm.view`, `vm.lifecycle`, `vm.snapshot`, `vm.console`, `vm.edit`, `host.view`, `storage.view`, `storage.upload` |
| Viewer | `vm.view`, `host.view`, `storage.view` |

Custom roles are persisted — built-ins are managed and can't be
deleted or have their permissions edited.

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
