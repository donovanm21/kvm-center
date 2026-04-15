---
title: Getting started
section: Getting started
order: 20
---

# Getting started

A fresh kvm-center install needs three things: the control-plane
stack, at least one enrolled hypervisor, and an administrator
account.

## 1. Deploy the control plane

```sh
# On the docker host
git clone <repo> /opt/kvm-center
cd /opt/kvm-center
scripts/bootstrap.sh --deploy
```

The `--deploy` path generates a Mongo password on first run, builds
the Docker stack, starts the containers, and pushes agent binaries
to known hypervisors. The build is safe to run repeatedly — the
`GIT_SHA` build arg invalidates the test + compile layers on every
new commit so you always get a fresh test run.

Ports:

| Port | Protocol | Purpose |
|------|----------|---------|
| 8090 | HTTP     | Agent telemetry + API + `/downloads/*` (binaries + install scripts, reachable before an agent trusts the cluster CA) |
| 8190 | HTTPS    | Web UI + API (operator-facing) |

## 2. Create the admin account

Visit `https://<server>:8190` in a browser. First use asks for a new
administrator: pick a username and password (min 8 chars). 2FA (TOTP)
can be enabled from the user menu → **Security & 2FA**.

## 3. Enroll a hypervisor

Every hypervisor needs an identity certificate signed by the cluster
CA. Two steps:

1. **Mint an enrollment token** — Settings → TLS → Hypervisor PKI,
   enter the host ID (use the FQDN — it becomes the cert CN), click
   **Issue enrollment token**. Copy the `enroll_…` string. One-time,
   10-minute TTL.

2. **Run the installer** on the hypervisor:

   ```sh
   curl -fsSL http://<server>:8090/downloads/install-agent.sh | bash -s -- \
     --server http://<server>:8090 \
     --host-id hv01.example.com \
     --enroll-token enroll_…
   ```

   The script drops the binary into `/opt/kvm-center`, creates a
   systemd unit, exchanges a CSR for the signed leaf cert, installs
   the libvirt TLS bundle, flips `listen_tls=1`, opens the firewall
   for 16514 + 49152-49215, and installs the `/etc/libvirt/hooks/qemu`
   grant enforcement hook.

The host appears in the dashboard within 30 seconds. An **mTLS** pill
on the host detail page confirms the cert is live.

## 4. Set up the CLI (optional)

```sh
curl -fsSL http://<server>:8090/downloads/install-kvmctl.sh | bash -s -- \
  --portal http://<server>:8090/downloads
# then mint a token in the user menu → My API tokens
kvmctl login --server https://<server>:8190 --token kvmc_…
kvmctl hosts
```

## 5. Configure notifications + metrics (optional)

- **Settings → Notifications** — webhook (Discord / Slack / generic)
  or email channels, pick which events fire.
- **Settings → Metrics** — InfluxDB v2 URL / org / bucket / token,
  import the Grafana dashboard from the download link.

You're in business.
