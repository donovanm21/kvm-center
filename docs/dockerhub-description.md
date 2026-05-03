# kvm-center

**Central control plane for KVM/libvirt hypervisors.** Manage a fleet
of KVM hosts from one web UI: live + offline migrations, snapshots,
inventory, RBAC, audit, real-time tasks panel, mobile-friendly SPA.
Single Go binary server + ~7 MB agents per host. MongoDB for
persistence; SQLite for the agent's per-host operation store.

> ⚠ This image is **private during development**. `docker login -u thedon21`
> first. Will be flipped public in a future release.

## Quick start

```sh
docker login -u thedon21
docker run -d --name kvm-center -p 8190:8443 \
  -v kvm-center-data:/var/lib/kvm-center \
  -e KVM_CENTER_PUBLIC_URL=https://localhost:8190 \
  thedon21/kvm-center:latest
```

Then open `https://localhost:8190` and walk the setup wizard.

## docker-compose (with MongoDB)

```yaml
services:
  mongo:
    image: mongo:7
    volumes: [mongo-data:/data/db]
    environment:
      MONGO_INITDB_ROOT_USERNAME: root
      MONGO_INITDB_ROOT_PASSWORD: ${MONGO_PASSWORD:?set MONGO_PASSWORD}
  server:
    image: thedon21/kvm-center:latest
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
MONGO_PASSWORD=$(openssl rand -base64 24) docker compose up -d
```

Update later with `docker compose pull && docker compose up -d`.

## Tags

| Tag | Tracks |
|---|---|
| `:latest` | last published minor / major release (rolling) |
| `:5.49` | last 5.49.x patch |
| `:5` | last 5.x release |
| `:vX.Y.Z` | exact release, immutable |

## Verifying the image (cosign)

Every published image is cosign-signed. Public key in the GitHub
repo at [`docs/security/cosign.pub`](https://github.com/donovanm21/kvmcenter/blob/main/docs/security/cosign.pub):

```sh
cosign verify \
  --key https://raw.githubusercontent.com/donovanm21/kvmcenter/main/docs/security/cosign.pub \
  thedon21/kvm-center:v5.49.0
```

## Architectures

`linux/amd64`, `linux/arm64`. Trying it on M-series Mac? Add
`--platform linux/amd64` if you hit any cross-arch issues.

## Source + docs

- Repo: https://github.com/donovanm21/kvmcenter
- Full deployment guide: https://github.com/donovanm21/kvmcenter/blob/main/server/docs/deployment.md
- Operations runbook: https://github.com/donovanm21/kvmcenter/blob/main/server/docs/operations.md
- Changelog: https://github.com/donovanm21/kvmcenter/blob/main/server/docs/changelog.md
