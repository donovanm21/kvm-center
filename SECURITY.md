# Security Policy

## Reporting a vulnerability

**Please do not open a public GitHub issue for security reports.** A
public issue discloses the problem to everyone before a fix is
available.

The canonical reporting channel is **GitHub private vulnerability
reporting** (Security Advisories). Open a private report here:

> **https://github.com/donovanm21/kvm-center/security/advisories/new**

(equivalently, the *Report a vulnerability* button under the
repository's **Security** tab). This creates a private thread visible
only to the maintainers, where we can discuss and coordinate a fix and
disclosure.

If you genuinely cannot use GitHub Security Advisories, email
`security@example.com` *(maintainers: replace this with your monitored
security address)*. Prefer the advisory flow whenever possible — it
keeps the report, the fix, and the disclosure timeline in one place.

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
