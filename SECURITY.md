# Security Policy

## Reporting a vulnerability

**Please do not open a public GitHub issue for security reports.**

If you believe you have found a security issue in kvm-center, report it
privately via one of:

- **GitHub Security Advisories** — use the *Report a vulnerability*
  button on the repository's Security tab. This is the preferred
  channel; it creates a private thread visible only to maintainers.
- **Email** — open a regular (non-sensitive) issue asking for a
  contact address if you cannot use GitHub Security Advisories.

When reporting, please include:

- A description of the issue and the component affected (server,
  agent, kvm-tools, kvmctl, web UI, installer).
- Steps to reproduce, or a proof-of-concept if you have one.
- The commit SHA or release tag you tested against.
- Any suggested remediation.

We aim to acknowledge reports within **7 days** and will keep you
updated as we investigate and fix. We ask that you give us a
reasonable window to ship a fix before public disclosure — typically
**90 days**, shorter if we agree the severity warrants it.

## Scope

In scope: the code in this repository — server, agent, kvm-tools,
kvmctl, web UI, install scripts, docker-compose manifest.

Out of scope: third-party dependencies (report those upstream), the
operator's own deployment configuration, and issues that require
physical or administrative access to the host that kvm-center is
already trusted on.

## Supported versions

Only the latest tagged release receives security fixes. If you are on
an older release, please upgrade before filing a report.

## Hardening notes

kvm-center is designed for an operator-trusted environment (you are
the admin of your own hypervisors). Before exposing the web UI beyond
a private network, review:

- TLS configuration (`Settings → TLS`) — the server ships with a
  self-signed cert on first boot; replace it with a real certificate.
- Authentication providers (`Settings → Providers`) — the built-in
  password auth is fine for small teams; SAML / LDAP are supported for
  larger ones.
- The *Public URL* setting — set this so install snippets and webhook
  deep links resolve to your real hostname, not an arbitrary URL.
- Network exposure — `:8190` is TLS and is the only port the product
  listens on since v5.32.5. The previous plaintext `:8090` fallback
  was retired after a fleet-wide rotation onto HTTPS; the server
  refuses to bind it unless `KVM_CENTER_DISABLE_HTTP=0` is set as
  an explicit rollback.
- Self-signed certificates — the default cert is trusted by agents
  via `InsecureSkipVerify` with a warning logged on every report.
  This is fine for a homelab but is a real man-in-the-middle exposure
  on untrusted networks; install a trusted certificate via
  **Settings → TLS** before exposing the server across network
  boundaries you don't control.
