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

Three things every reverse proxy needs to forward correctly:

1. **`X-Forwarded-For`** — for audit-log accuracy and per-IP login throttling. Set `KVM_CENTER_TRUSTED_PROXIES=<proxy-CIDR>` on the kvm-center container so the header is honoured.
2. **WebSocket / SSE upgrade** — `/api/events` (the inventory event stream) and `/api/console-ws/<vm>` (in-browser VNC) both keep long-lived connections. Without `Upgrade` / `Connection: upgrade` headers they fall back to short-poll and feel broken.
3. **Authorization + Origin headers** — passed through unmodified. CORS is closed by default; if the SPA is hosted at a different origin from the API set `KVM_CENTER_CORS_ORIGINS=https://kvm.example.com`.

#### nginx

```nginx
server {
    listen 443 ssl http2;
    server_name kvm.example.com;

    ssl_certificate     /etc/letsencrypt/live/kvm.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/kvm.example.com/privkey.pem;

    # Optional: enforce SSO before the request reaches kvm-center.
    # Either keep this and have kvm-center trust the upstream user, OR
    # configure kvm-center's built-in OIDC/SAML and remove these lines.
    auth_request /_oauth2/auth;
    error_page 401 = /_oauth2/sign_in;

    location / {
        proxy_pass         https://kvm-center.internal:8190;
        proxy_ssl_verify   off;       # self-signed upstream cert is fine

        proxy_set_header   Host              $host;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto https;

        # WebSocket + SSE upgrade — needed for /api/events + console.
        proxy_http_version 1.1;
        proxy_set_header   Upgrade           $http_upgrade;
        proxy_set_header   Connection        $connection_upgrade;

        # SSE keeps connections open for hours; default 60s read timeout
        # disconnects the inventory stream. Bump to match your max session.
        proxy_read_timeout  3600s;
        proxy_buffering     off;
    }
}

map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}
```

#### Caddy

```caddyfile
kvm.example.com {
    tls /etc/caddy/kvm.crt /etc/caddy/kvm.key

    reverse_proxy https://kvm-center.internal:8190 {
        transport http {
            tls_insecure_skip_verify
            read_timeout 1h
        }
        # X-Forwarded-* are added by Caddy automatically.
        flush_interval -1   # no buffer for SSE / VNC console.
    }
}
```

Caddy handles the `Upgrade` / `Connection` dance internally — no extra config needed for SSE or WebSockets.

#### Traefik (v3, file provider)

```yaml
http:
  routers:
    kvm-center:
      rule: Host(`kvm.example.com`)
      entryPoints: [websecure]
      service: kvm-center
      tls:
        certResolver: letsencrypt

  services:
    kvm-center:
      loadBalancer:
        servers:
          - url: https://kvm-center.internal:8190
        # Self-signed upstream — skip cert verification on the LAN hop.
        serversTransport: skip-verify
        # SSE / WebSocket: long-lived streams, no buffering.
        passHostHeader: true
        responseForwarding:
          flushInterval: -1

  serversTransports:
    skip-verify:
      insecureSkipVerify: true
```

Traefik forwards the standard `X-Forwarded-*` set by default; no extra middleware required for header pass-through. For audit-IP accuracy add the proxy's source CIDR to `KVM_CENTER_TRUSTED_PROXIES`.

kvm-center's built-in IdP (OIDC, SAML, LDAP — see [Authentication](authentication.md)) can also sit directly in front; use that when you don't want a separate edge proxy.

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
