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
| **[KVM appliance (qcow2)](#kvm-appliance)** | ~3 minutes | dedicated control-plane VM in your existing KVM fleet (v5.49.1+) |
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

> Available from **v5.49.1**. See the v5.49.1 release on
> [GitHub Releases](https://github.com/donovanm21/kvmcenter/releases)
> for the qcow2 + signature.

Coming together in v5.49.1 — operator import flow + virt-install
recipe will land here when the appliance build pipeline is wired up.

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
  Appliance qcow2 (v5.49.1+). One disk image, `virt-install --import`,
  done.
- **Custom Linux distro, ops shop with a config-management
  pipeline?** Bare-metal `install.sh`. The agent is a single 7 MB
  Go binary, the server is a single 30 MB binary; trivial to wrap
  in Ansible / Salt / whatever.

All four paths give you the same kvm-center; switching later is just
"point the new install at the same Mongo (or restore a backup) and
shut down the old one."
