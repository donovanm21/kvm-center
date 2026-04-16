---
title: Remote access
section: Operations
order: 66
---

# Remote access

kvm-center's control plane is HTTPS on 8190 and that's the only
port you should publish. Everything below is about how to get
operators to that port safely when they're not on the management
VLAN.

## The short version

- **Do** put the control plane behind a reverse proxy (nginx,
  Caddy, Traefik) with a real TLS cert and publish only 443.
- **Do** require an identity gateway (SSO, OIDC, or a VPN) in
  front of the proxy — kvm-center's own login page is fine on LAN
  but shouldn't be the only thing between the internet and `root`
  on your fleet.
- **Don't** port-forward 8190 directly to the internet.
- **Don't** expose 8081 (agent), 16514 (libvirt), 8090 (legacy
  plaintext), or MongoDB (27017) to anything but the management
  VLAN. None of those should ever route off-LAN. The VM console
  WebSocket is server-proxied on `:8190`, so publishing `:8081`
  is never required for consoles — see "VM console access" in
  [Authentication](authentication.md) for the full control stack.

## Recommended topologies

### Homelab / small team — WireGuard + LAN

Simplest and what this project's reference deployment uses. A
WireGuard tunnel into the LAN; operators then browse to the
control plane on its RFC1918 address.

- Zero inbound ports on the kvm-center host.
- The existing self-signed cert is fine — you're never on a
  hostile network when you hit it.
- `kvmctl` from a laptop works the same way: the VPN is up, the
  API is reachable.

### Small business — reverse proxy + SSO

Run nginx / Caddy / Traefik on an edge host with a real cert
(Let's Encrypt, internal CA, whatever). Enforce SSO at the proxy
so the kvm-center login page never sees an unauthenticated
request.

Example nginx snippet:

```nginx
server {
    listen 443 ssl http2;
    server_name kvm.example.com;

    ssl_certificate     /etc/letsencrypt/live/kvm.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/kvm.example.com/privkey.pem;

    # Enforce SSO before the request ever reaches kvm-center.
    auth_request /_oauth2/auth;
    error_page 401 = /_oauth2/sign_in;

    location / {
        proxy_pass         https://kvm-center.internal:8190;
        proxy_ssl_verify   off;       # self-signed backend
        proxy_set_header   Host              $host;
        proxy_set_header   X-Forwarded-For   $remote_addr;
        proxy_set_header   X-Forwarded-Proto https;
        proxy_http_version 1.1;
    }
}
```

kvm-center's built-in IdP (OIDC, SAML, LDAP — see [Authentication](authentication.md))
can also sit directly in front; use that when you don't want a
separate edge proxy.

### Larger / regulated — bastion + Zero Trust gateway

Terminate a Zero Trust gateway (Cloudflare Access, Tailscale ACLs,
Teleport) in front of the reverse proxy. The control plane stays
private; the gateway enforces device posture, MFA, and audit.

**Do not** rely on kvm-center's session cookies alone from an
untrusted network. Session tokens are authentication, not
authorization-at-the-edge.

## What about the hypervisors?

Operators almost never SSH to hypervisors directly under normal
operation — everything routes through the control plane. For the
rare break-glass case:

- Put hypervisors behind the same bastion as the control plane.
- Use key-based auth and a dedicated identity file routed through
  a bastion `ProxyJump` in your `~/.ssh/config`.
- Disable password auth on every hv* box once enrollment is done.
- Never publish 8081, 16514, or 49152-49215 off the management
  VLAN. They speak mTLS, but the attack surface of libvirtd and
  QEMU's migration protocol is not something you want hanging on
  the public internet.

## kvmctl over remote access

`kvmctl` just talks to the same HTTPS endpoint. If operators reach
the UI via a reverse proxy at `https://kvm.example.com`, configure
kvmctl the same way:

```sh
kvmctl config set-server https://kvm.example.com
kvmctl config set-token  kvmc_…
```

API tokens (Settings → API Tokens) are the right credential for
scripting — never bake a session cookie or a user password into a
CI job.

## Audit + visibility

Whatever edge you pick, make sure its access logs flow into the
same place as kvm-center's audit log. The in-app audit (Settings
→ Audit) records every state-changing action; pairing it with
edge logs gives you the full "who dialed in from where, then did
what" trail. `/api/settings/audit-shipping` forwards audit events
to syslog or an HTTP collector if you want them in Loki / Splunk /
Elastic.
