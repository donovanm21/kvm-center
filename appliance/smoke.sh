#!/usr/bin/env bash
# Boot a freshly-built kvm-center appliance qcow2 + verify it serves
# /api/version with the expected version string. Used by:
#   - `make smoke` for local dev validation,
#   - .gitea/workflows/appliance-build.yml as the pre-publish gate.
#
# Approach:
#   1. Generate a tiny cloud-init seed ISO via cloud-localds.
#   2. Boot the qcow2 with qemu-system-x86_64 (-nographic, daemonized).
#   3. Discover the guest IP via `virsh domifaddr` (or DHCP lease in
#      a tap mode that we can introspect). For simplicity here we use
#      user-mode networking with hostfwd 18190->8443 so the host can
#      curl localhost:18190.
#   4. Poll https://localhost:18190/api/version for up to 120 s.
#   5. Report + clean up.
#
# Usage:
#   ./smoke.sh <qcow2> <expected-version>

set -euo pipefail

QCOW="${1:?qcow2 path required}"
VERSION="${2:?expected version required}"

[ -f "$QCOW" ] || { echo "ERROR: $QCOW not found"; exit 1; }

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"; pkill -f "qemu-system-x86_64.*kvmc-smoke" 2>/dev/null || true' EXIT

# Generate cloud-init seed.
cat > "$WORK/user-data" <<EOF
#cloud-config
hostname: kvmc-smoke
ssh_pwauth: true
chpasswd:
  list: |
    root:smoke
  expire: false
EOF
echo -e "instance-id: kvmc-smoke\nlocal-hostname: kvmc-smoke" > "$WORK/meta-data"
cloud-localds "$WORK/seed.iso" "$WORK/user-data" "$WORK/meta-data"

# Snapshot copy so we don't mutate the published artifact.
cp --reflink=auto "$QCOW" "$WORK/disk.qcow2" || cp "$QCOW" "$WORK/disk.qcow2"

# Launch in background, user-mode net with hostfwd to the TLS port.
qemu-system-x86_64 \
  -name kvmc-smoke \
  -machine q35,accel=kvm:tcg \
  -cpu host \
  -m 2048 -smp 2 \
  -drive file="$WORK/disk.qcow2",if=virtio,format=qcow2 \
  -drive file="$WORK/seed.iso",if=virtio,format=raw,readonly=on \
  -netdev user,id=n0,hostfwd=tcp::18190-:8443 \
  -device virtio-net-pci,netdev=n0 \
  -nographic \
  -daemonize \
  -serial file:"$WORK/console.log" \
  -monitor null

echo ">>> appliance booting (console: $WORK/console.log) — polling https://localhost:18190/api/version"

ok=0
for i in $(seq 1 120); do
  got=$(curl -fsk https://localhost:18190/api/version 2>/dev/null \
    | python3 -c "import json,sys; print(json.load(sys.stdin).get('version',''))" 2>/dev/null \
    || true)
  if [ "$got" = "$VERSION" ]; then
    ok=1
    echo ">>> smoke OK after ${i}s — appliance returns $got"
    break
  fi
  sleep 1
done

if [ "$ok" -ne 1 ]; then
  echo ">>> tail of guest console:"
  tail -50 "$WORK/console.log" 2>/dev/null || true
  echo ">>> ERROR: smoke FAILED — appliance did not return $VERSION on /api/version within 120s"
  exit 1
fi
