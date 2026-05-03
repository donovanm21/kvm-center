---
title: Deployment
section: Getting started
order: 25
---

# Deployment

Three ways to run kvm-center, in order of how quickly you can be poking
at the SPA from a fresh machine:

| Path | Time to running | Best for |
|---|---|---|
| **[Docker / docker-compose](#docker)** | ~1 minute | trying it out, single-host deploys, dev setups |
| **[KVM appliance (qcow2)](#kvm-appliance)** | ~3 minutes | dedicated control-plane VM in your existing KVM fleet (v5.49.3+) |
| **[Bare-metal `install.sh`](#bare-metal)** | ~10 minutes | custom OS, ops shop with a config-management story |

Each path uses the same images / binaries — once you've installed,
the upgrade flow is "pull a new image / qcow2 / binary, restart" with
no data migration. Pick on convenience, not lock-in.

## Docker

> ⚠ **The `thedon21/kvm-center` repo is currently private during
> development.** You'll need `docker login -u thedon21` first; the
> maintainer will share access on request. Will be flipped public in
> a future release.

### One-liner (no Mongo — JSON file persistence)

```sh
docker login -u thedon21
docker run -d --name kvm-center -p 8190:8443 \
  -v kvm-center-data:/var/lib/kvm-center \
  -e KVM_CENTER_PUBLIC_URL=https://localhost:8190 \
  thedon21/kvm-center:latest
```

Open `https://localhost:8190`, accept the self-signed cert, walk the
captive setup wizard. The `kvm-center-data` named volume holds your
state; back it up like any other Docker volume.

### docker-compose (with MongoDB — recommended)

`docker-compose.yml`:

```yaml
services:
  mongo:
    image: mongo:7
    restart: unless-stopped
    volumes: [mongo-data:/data/db]
    environment:
      MONGO_INITDB_ROOT_USERNAME: root
      MONGO_INITDB_ROOT_PASSWORD: ${MONGO_PASSWORD:?set MONGO_PASSWORD}

  server:
    image: thedon21/kvm-center:latest
    restart: unless-stopped
    depends_on: [mongo]
    ports: ["8190:8443"]
    environment:
      KVM_CENTER_MONGO_URL: mongodb://root:${MONGO_PASSWORD}@mongo:27017
      KVM_CENTER_PUBLIC_URL: https://localhost:8190
    volumes: [kvm-center-data:/var/lib/kvm-center]

volumes:
  mongo-data:
  kvm-center-data:
```

Bring it up:

```sh
docker login -u thedon21
MONGO_PASSWORD=$(openssl rand -base64 24) docker compose up -d
docker compose logs -f server
```

### Updating

```sh
docker compose pull && docker compose up -d
```

### Tag channels

| Tag | Tracks |
|---|---|
| `:latest` | last published minor / major release (rolling) |
| `:5.49` | last 5.49.x patch (stable channel) |
| `:5` | last 5.x release |
| `:vX.Y.Z` | exact release, immutable |

Pin `:vX.Y.Z` for production; pin `:5.49` if you want patch updates
without minor-version surprises.

### Verifying the image (cosign)

Every published image is signed. See
[`docs/security/README.md`](https://github.com/donovanm21/kvmcenter/blob/main/docs/security/README.md)
for the verification flow:

```sh
cosign verify \
  --key https://raw.githubusercontent.com/donovanm21/kvmcenter/main/docs/security/cosign.pub \
  thedon21/kvm-center:v5.49.0
```

## KVM appliance

> Available from **v5.49.3**. Pre-built qcow2 images live on the
> [GitHub Releases](https://github.com/donovanm21/kvmcenter/releases)
> page — each release ships the qcow2, a `SHA256SUMS` file, and a
> `SHA256SUMS.asc` GPG signature.

The appliance is a Rocky Linux 9 minimal image with kvm-center,
MongoDB, and a one-shot first-boot service that generates a random
admin password and prints it to the libvirt console + SSH MOTD. No
config to write — `virt-install --import` and you're up.

### Prerequisites

A KVM hypervisor with `virt-install` (libvirt-client + virt-install
package), the `gh` CLI for downloading from a private/public
release, and `gpg` for signature verification. All three are in
every mainstream distro.

### Step 1 — Download

```sh
V=v5.49.3   # pick a release from
            # https://github.com/donovanm21/kvmcenter/releases
gh release download "$V" --repo donovanm21/kvmcenter \
  --pattern 'kvm-center-appliance-*.qcow2' \
  --pattern 'SHA256SUMS*'
```

If you don't use `gh`, the equivalent `curl` URLs are
`https://github.com/donovanm21/kvmcenter/releases/download/$V/<filename>`.

### Step 2 — Verify the signature + checksum

The first time, import the maintainer GPG key (one-off):

```sh
curl -fsSL https://raw.githubusercontent.com/donovanm21/kvmcenter/main/docs/security/maintainer.pub.asc \
  | gpg --import
```

Then verify both the signature and the checksum every time:

```sh
gpg --verify SHA256SUMS.asc SHA256SUMS    # must say "Good signature"
sha256sum -c SHA256SUMS                   # must say "OK"
```

If either fails, **stop** — don't import an image whose chain of
custody you can't prove.

### Step 3 — Import into libvirt

```sh
sudo cp kvm-center-appliance-${V}.qcow2 /var/lib/libvirt/images/

sudo virt-install --name kvm-center \
  --memory 2048 --vcpus 2 \
  --disk path=/var/lib/libvirt/images/kvm-center-appliance-${V}.qcow2,bus=virtio \
  --import --os-variant rocky9 \
  --network bridge=virbr0 \
  --noautoconsole
```

Adjust `--network` if you bridge to a real LAN (e.g.
`bridge=br0`) instead of the libvirt default NAT.

`virt-install --import` takes ~5 seconds; the first boot
(generates passwords, starts MongoDB + kvm-center) is another
30-60 seconds.

### Step 4 — Grab the admin password from the console

```sh
sudo virsh console kvm-center
```

You'll see a banner:

```
╔══════════════════════════════════════════════════════════╗
║  kvm-center appliance                                    ║
║                                                          ║
║  URL:            https://192.168.122.42:8190             ║
║  admin password: <32 random chars — copy this then rotate>║
║                                                          ║
║  Reset password from the SPA → Settings → Users.         ║
║  This banner clears on next boot.                        ║
╚══════════════════════════════════════════════════════════╝
```

URL + password also land in the SSH MOTD if you `ssh root@<ip>`
instead of using the libvirt console. Open the URL, log in with
`admin` + that password, walk the setup wizard. **Rotate the
password** from Settings → Users — the bootstrap password is
single-use and the banner clears on the next boot.

### Sizing

The defaults in the `virt-install` snippet (2 vCPU / 2 GiB RAM /
~10 GiB disk in the qcow2) cover a fleet of up to ~50 hypervisors
and ~500 VMs. Bump `--memory` first if you hit MongoDB pressure;
the qcow2 grows up to ~40 GiB before you'd consider attaching
external storage.

### Upgrading

```sh
V=v5.49.4   # the new release
gh release download "$V" --repo donovanm21/kvmcenter \
  --pattern 'kvm-center-appliance-*.qcow2' \
  --pattern 'SHA256SUMS*'
gpg --verify SHA256SUMS.asc SHA256SUMS
sha256sum -c SHA256SUMS

sudo virsh shutdown kvm-center
# wait until `virsh list --all` shows it shut off
sudo virsh undefine kvm-center                                  # keeps disks
sudo cp kvm-center-appliance-${V}.qcow2 /var/lib/libvirt/images/

# Same virt-install command as Step 3, pointed at the new qcow2.
```

The MongoDB data dir lives inside the qcow2 — to keep your fleet
inventory across upgrades, attach a persistent data disk before
the first boot or take MongoDB backups via
[the backup runbook](backup) and restore after import. A future
release will land an in-place upgrade flow that swaps just the
binary; for now, use the Docker path if you want zero-downtime
upgrades.

### Verifying the qcow2 boots what it claims

```sh
ssh root@<appliance-ip> 'kvm-center-server --version'
# or, from the SPA:
curl -k https://<appliance-ip>:8190/api/version
```

## Bare metal

The original install path. Pulls the binary from your existing
kvm-center server's `/downloads/` portal:

```sh
curl -fsSLk https://<your-kvm-center-server>:8190/downloads/install.sh | sudo bash
```

The script auto-detects whether you're on a hypervisor (installs
the agent) or a guest VM (installs kvm-tools). Custom flags + the
full inventory are documented in [Getting started](getting-started).

## Choosing a path

- **Just want to try kvm-center on your laptop?** Docker one-liner.
  Wipe later with `docker rm -f kvm-center && docker volume rm kvm-center-data`.
- **Single-host homelab, comfortable with Docker?** docker-compose.
  Mongo gives you the proper persistence layer; volume backups are
  trivial.
- **Already running KVM and want a dedicated control-plane VM?**
  Appliance qcow2 (v5.49.3+). One disk image, `virt-install --import`,
  done.
- **Custom Linux distro, ops shop with a config-management
  pipeline?** Bare-metal `install.sh`. The agent is a single 7 MB
  Go binary, the server is a single 30 MB binary; trivial to wrap
  in Ansible / Salt / whatever.

All four paths give you the same kvm-center; switching later is just
"point the new install at the same Mongo (or restore a backup) and
shut down the old one."
