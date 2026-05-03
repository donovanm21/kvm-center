#!/usr/bin/env bash
# Build the kvm-center KVM appliance qcow2 (v5.49.3+).
#
# Pipeline:
#   1. Build kvm-center-server binary locally via Dockerfile.server
#      (multi-stage build; we only want the final /app/kvm-center-server).
#   2. virt-builder rocky-9.4 base with our customisations:
#        - dnf install mongodb (so MongoDB is bundled, not pulled at boot)
#        - copy the binary + /downloads/dist tree into the image
#        - install systemd units (kvm-center.service, mongod is upstream
#          + kvm-center-firstboot.service for one-shot setup)
#        - touch /etc/machine-id (forces regenerate on first boot so
#          duplicate VMs from one image don't share an ID)
#   3. virt-sparsify the resulting qcow2.
#   4. Output: kvm-center-appliance-vX.Y.Z.qcow2 in $OUT (default ./out/).
#
# Run on a Linux host with libguestfs-tools + libguestfs-tools-c
# installed. macOS support is not planned (libguestfs is Linux-only).
# The CI workflow at .gitea/workflows/appliance-build.yml provides
# the build host for the published artifact.
#
# Usage:
#   ./build.sh [output-dir]
# Env vars:
#   APPLIANCE_BASE_OS  override of virt-builder OS slug (default: rockylinux-9.4)
#   APPLIANCE_SIZE     output image size (default: 8G)
#   APPLIANCE_MEMORY   build-time memory for guestfs (default: 2048)

set -euo pipefail

cd "$(dirname "$0")"

OUT="${1:-./out}"
BASE_OS="${APPLIANCE_BASE_OS:-rockylinux-9.4}"
SIZE="${APPLIANCE_SIZE:-8G}"
MEMORY="${APPLIANCE_MEMORY:-2048}"

# Source the version from the canonical location.
VERSION=$(tr -d '[:space:]' < ../server/VERSION)
[ -z "$VERSION" ] && { echo "ERROR: cannot read ../server/VERSION"; exit 1; }

OUTPUT_QCOW="$OUT/kvm-center-appliance-${VERSION}.qcow2"

mkdir -p "$OUT"
echo ">>> building kvm-center-appliance ${VERSION}"
echo ">>> base OS:   $BASE_OS"
echo ">>> size:      $SIZE"
echo ">>> output:    $OUTPUT_QCOW"
echo

# ── Step 1: build the kvm-center binary + dist artifacts via Docker ──
# Re-use Dockerfile.server's builder + artifacts stages so the
# appliance gets bit-identical artifacts to the Docker Hub image.
STAGE=/tmp/kvm-center-appliance-stage
rm -rf "$STAGE" && mkdir -p "$STAGE"

echo ">>> [1/4] building Dockerfile.server (re-uses cached layers when possible)"
docker build -f ../Dockerfile.server -t kvmc-appliance-staging:dev .. >/dev/null

echo ">>> [2/4] extracting binary + dist from staging image"
cid=$(docker create kvmc-appliance-staging:dev)
docker cp "$cid:/app/kvm-center-server" "$STAGE/kvm-center-server"
docker cp "$cid:/app/dist"              "$STAGE/dist"
docker rm "$cid" >/dev/null

# Sanity: extracted binary reports the expected version.
got=$("$STAGE/kvm-center-server" -version 2>&1 | head -1 || true)
case "$got" in
  *"$VERSION"*) ;;
  *) echo "ERROR: extracted binary reports '$got', not $VERSION"; exit 1;;
esac

# ── Step 3: virt-builder the qcow2 ──
echo ">>> [3/4] virt-builder $BASE_OS → $OUTPUT_QCOW"
virt-builder "$BASE_OS" \
  --output "$OUTPUT_QCOW" \
  --format qcow2 \
  --size "$SIZE" \
  --memsize "$MEMORY" \
  --hostname kvm-center \
  --timezone Etc/UTC \
  --update \
  --install mongodb-org-server,mongodb-org-tools \
  --copy-in "$STAGE/kvm-center-server:/usr/local/bin/" \
  --copy-in "$STAGE/dist:/app/" \
  --mkdir /var/lib/kvm-center \
  --mkdir /etc/kvm-center \
  --upload "systemd/kvm-center.service:/etc/systemd/system/" \
  --upload "systemd/kvm-center-firstboot.service:/etc/systemd/system/" \
  --upload "systemd/kvm-center-firstboot.sh:/usr/local/sbin/" \
  --run-command 'chmod +x /usr/local/bin/kvm-center-server /usr/local/sbin/kvm-center-firstboot.sh' \
  --run-command 'systemctl enable mongod kvm-center kvm-center-firstboot' \
  --run-command 'firewall-offline-cmd --add-port=8190/tcp' \
  --run-command 'truncate -s 0 /etc/machine-id' \
  --selinux-relabel

# ── Step 4: sparsify ──
echo ">>> [4/4] sparsify"
SPARSIFIED="${OUTPUT_QCOW}.sparse"
virt-sparsify --convert qcow2 "$OUTPUT_QCOW" "$SPARSIFIED"
mv "$SPARSIFIED" "$OUTPUT_QCOW"

echo
echo ">>> built: $OUTPUT_QCOW"
ls -lh "$OUTPUT_QCOW"

# Cleanup the staging dir; the Docker image layer cache stays.
rm -rf "$STAGE"
