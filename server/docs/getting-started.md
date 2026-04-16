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

For a first-time install the recommended path is the interactive
installer — it prompts for Mongo credentials, the cluster DNS
domain, and writes a `.env` file (mode `0600`) before kicking off
the deploy:

```sh
# On the docker host
git clone <repo> /opt/kvm-center
cd /opt/kvm-center
scripts/bootstrap.sh --interactive
```

Every prompt has a safe default, and the captured Mongo password
is unset from the shell immediately after it's written to `.env`
so it can't leak into `env` / `ps` output.

**Re-running `--interactive`** on a host that already has an
install gives you four choices up-front:

- **keep** — just re-run `--deploy` against the current state (no
  data loss; use this for routine re-deploys).
- **reconfig** — regenerate `.env` (extra-confirmed because the
  existing Mongo volume was initialised against the old password
  and won't authenticate).
- **fresh** — destructive nuke: `docker compose down -v`, delete
  `.env` and any `.env.bak.*`, start over. Double-confirmed.
  Hypervisor-side data is never touched.
- **cancel** — no-op.

For automated redeploys (CI, SSH), skip the TUI:

```sh
scripts/bootstrap.sh --deploy
```

`--deploy` pulls the latest commit, rebuilds the Docker stack,
starts the containers, and pushes fresh agent binaries to known
hypervisors. The build is safe to run repeatedly — the `GIT_SHA`
build arg invalidates the test + compile layers on every new
commit so you always get a fresh test run.

Ports:

| Port | Protocol | Purpose |
|------|----------|---------|
| 8090 | HTTP     | Agent telemetry + API + `/downloads/*` (binaries + install scripts, reachable before an agent trusts the cluster CA) |
| 8190 | HTTPS    | Web UI + API (operator-facing) |

## 2. Create the admin account and run the setup wizard

Visit `https://<server>:8190` in a browser. First use asks for a new
administrator: pick a username and password (min 8 chars). 2FA (TOTP)
can be enabled later from the user menu → **Security & 2FA**.

Once the admin account is created, the SPA routes to **`/setup`**
and walks you through four optional steps:

1. **Welcome** — confirmation that the stack is healthy.
2. **First hypervisor** — mints a one-time enrollment token and
   shows the install one-liner to run on the hypervisor.
3. **Notifications** — optional Discord / Slack / generic webhook.
4. **Metrics** — optional InfluxDB v2 URL + token.

Every step has a "Skip the rest — take me to the dashboard" button;
the stack works fine with the defaults, and everything the wizard
touches is available later under **Settings**.

## 3. Enroll a hypervisor

Every hypervisor needs an identity certificate signed by the cluster
CA. Two steps:

1. **Mint an enrollment token** — Settings → TLS → Hypervisor PKI,
   enter the host ID (use the FQDN — it becomes the cert CN), click
   **Issue enrollment token**. Copy the `enroll_…` string. One-time,
   10-minute TTL.

2. **Run the installer** on the hypervisor:

   ```sh
   curl -fsSLk https://<server>:8190/downloads/install-agent.sh | bash -s -- \
     --server https://<server>:8190 \
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
curl -fsSLk https://<server>:8190/downloads/install-kvmctl.sh | bash -s -- \
  --portal https://<server>:8190/downloads
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
