# kvm-center appliance — developer guide

This directory builds the kvm-center qcow2 published to GitHub
Releases (v5.49.3+). It's how new operators try kvm-center
without thinking about Linux distro picks, Docker layers, or
TLS bootstrap — they download the qcow2, run `virt-install
--import`, and have a working install in ~3 minutes.

If you're an operator who just wants to **use** the appliance,
see `server/docs/deployment.md` instead. This doc is for people
modifying the build pipeline.

## What's in here

| File | Role |
|------|------|
| `build.sh` | The build pipeline. Calls `virt-builder` against `centosstream-9` (RHEL-9 family; libguestfs upstream stopped shipping Rocky in early 2026), layers in the kvm-center binary + dist + systemd units, sparsifies. |
| `smoke.sh` | Boots the freshly-built qcow2 with cloud-init + qemu user-mode net + hostfwd, polls `/api/version` for up to 120 s. The CI gate. |
| `Makefile` | Wraps `build.sh` + `smoke.sh` so `make build && make smoke` is the local flow. |
| `cloud-init/{user-data,meta-data}` | Seed data for `smoke.sh` only. The published qcow2 does not ship with these — the appliance generates its own admin credentials on first boot via the firstboot systemd service. |
| `systemd/kvm-center.service` | Long-running service definition (kvm-center-server bound to MongoDB on the same VM). |
| `systemd/kvm-center-firstboot.service` + `.sh` | One-shot first-boot setup. Generates a random admin password, writes it to `/etc/issue` (libvirt console) and `/etc/motd` (SSH login banner). Marks itself complete via `/var/lib/kvm-center/.firstboot-done` so a reboot doesn't regenerate. |

## Building locally (Linux only)

Requires:

```sh
sudo dnf install -y libguestfs-tools-c qemu-img cloud-utils
# or on Debian/Ubuntu:
# sudo apt install -y libguestfs-tools qemu-utils cloud-image-utils
```

Then:

```sh
cd appliance/
make build       # ~10 min on a warm cache; produces ./out/kvm-center-appliance-vX.Y.Z.qcow2
make smoke       # ~2 min; boots the qcow2 + polls /api/version
```

`build.sh` re-uses `Dockerfile.server` to produce the kvm-center
binary + SPA dist (so you don't need a separate Go toolchain
locally — Docker handles that). The qcow2 is built fresh each
time; cache layers from Docker speed up subsequent builds.

## CI

`.gitea/workflows/appliance-build.yml` runs the same steps on a
privileged Gitea runner with libvirt available. Triggered by the
v5.49 publish-gate on minor / major version bumps + `[publish]`
patches. Output uploads to GitHub Releases via PR-2c's pipeline.

## Operator import flow (documented)

```sh
# 1. Download qcow2 + signature from the GitHub Release page.
gh release download vX.Y.Z --repo donovanm21/kvmcenter \
  --pattern 'kvm-center-appliance-*.qcow2*' \
  --pattern 'SHA256SUMS*'

# 2. Verify the signature + checksum.
gpg --verify SHA256SUMS.asc SHA256SUMS    # one-time gpg --import of docs/security/maintainer.pub.asc
sha256sum -c SHA256SUMS

# 3. Import + start.
sudo cp kvm-center-appliance-vX.Y.Z.qcow2 /var/lib/libvirt/images/
sudo virt-install --name kvm-center --memory 2048 --vcpus 2 \
  --disk path=/var/lib/libvirt/images/kvm-center-appliance-vX.Y.Z.qcow2,bus=virtio \
  --import --os-variant centos-stream9 --network bridge=virbr0 --noautoconsole

# 4. Read the admin password from the libvirt console.
sudo virsh console kvm-center
# Banner shows: URL = https://<dhcp-ip>:8190 + admin password.
```

## Upgrade flow

The appliance doesn't auto-update. To upgrade:

1. Download the new qcow2 + verify signature.
2. `virsh shutdown kvm-center && virsh undefine kvm-center` (keeps
   the disk file).
3. Replace the disk file at `/var/lib/libvirt/images/`.
4. Re-import with `virt-install --import`.

Persistent state lives at `/var/lib/kvm-center/` inside the VM.
Backup before upgrade via the kvm-center admin → Backup tab.
