#!/usr/bin/env bash
# install-agent.sh — Install and configure the kvm-center agent as a systemd service.
# Run as root on each KVM hypervisor.
#
# Usage:
#   sudo ./install-agent.sh --server http://<server-ip>:8090 [--port 8081] [--interval 30s]
#
set -euo pipefail

INSTALL_DIR="/opt/kvm-center"
BINARY_NAME="kvm-center-agent"
SERVICE_NAME="kvm-center-agent"
LOG_FILE="/var/log/kvm-center-agent.log"

# Defaults
SERVER_URL=""
AGENT_PORT=8081
INTERVAL="30s"
ARCH="$(uname -m)"

usage() {
  echo "Usage: $0 --server <url> [--port <port>] [--interval <duration>]"
  echo "  --server    Control plane URL (required), e.g. http://REDACTED-HOST:8090"
  echo "  --port      Agent command port (default: 8081)"
  echo "  --interval  Telemetry interval (default: 30s)"
  exit 1
}

# ── Parse arguments ────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --server)   SERVER_URL="$2"; shift 2 ;;
    --port)     AGENT_PORT="$2"; shift 2 ;;
    --interval) INTERVAL="$2";  shift 2 ;;
    --help|-h)  usage ;;
    *) echo "Unknown argument: $1"; usage ;;
  esac
done

[[ -z "$SERVER_URL" ]] && { echo "ERROR: --server is required"; usage; }

# ── Root check ────────────────────────────────────────────────────────────────
if [[ $EUID -ne 0 ]]; then
  echo "ERROR: This script must be run as root (use sudo)."
  exit 1
fi

# ── Detect architecture ───────────────────────────────────────────────────────
case "$ARCH" in
  x86_64)  ARCH_SUFFIX="amd64" ;;
  aarch64) ARCH_SUFFIX="arm64" ;;
  *)       echo "ERROR: Unsupported architecture: $ARCH"; exit 1 ;;
esac

# ── Find binary ────────────────────────────────────────────────────────────────
# Look for arch-specific binary first, then plain name in the same directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY=""
for candidate in \
  "${SCRIPT_DIR}/${BINARY_NAME}-${ARCH_SUFFIX}" \
  "${SCRIPT_DIR}/${BINARY_NAME}" \
  "./${BINARY_NAME}-${ARCH_SUFFIX}" \
  "./${BINARY_NAME}"; do
  if [[ -f "$candidate" ]]; then
    BINARY="$candidate"
    break
  fi
done

if [[ -z "$BINARY" ]]; then
  echo "ERROR: Could not find agent binary. Expected: ${BINARY_NAME} or ${BINARY_NAME}-${ARCH_SUFFIX}"
  echo "Download from your build server or run: make dist"
  exit 1
fi

echo "Installing kvm-center agent..."
echo "  Binary   : $BINARY (arch: $ARCH_SUFFIX)"
echo "  Server   : $SERVER_URL"
echo "  Port     : $AGENT_PORT"
echo "  Interval : $INTERVAL"
echo ""

# ── Install ────────────────────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"
cp "$BINARY" "${INSTALL_DIR}/${BINARY_NAME}"
chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

# ── Systemd unit ───────────────────────────────────────────────────────────────
cat > "/etc/systemd/system/${SERVICE_NAME}.service" << EOF
[Unit]
Description=KVM Center Agent
Documentation=https://github.com/your-org/kvm-center
After=network.target libvirtd.service
Wants=libvirtd.service

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/${BINARY_NAME} \\
  -server ${SERVER_URL} \\
  -port ${AGENT_PORT} \\
  -interval ${INTERVAL}
Restart=always
RestartSec=10
StandardOutput=append:${LOG_FILE}
StandardError=append:${LOG_FILE}

# Allow virsh access
SupplementaryGroups=libvirt kvm

[Install]
WantedBy=multi-user.target
EOF

# ── kvm-migrator user for live migration ──────────────────────────────────────
echo "Setting up kvm-migrator user for VM live migration..."
if ! id kvm-migrator &>/dev/null; then
  useradd -r -m -d /var/lib/kvm-migrator -s /bin/bash -G libvirt,kvm kvm-migrator
  echo "  Created kvm-migrator user"
else
  usermod -aG libvirt,kvm kvm-migrator 2>/dev/null
  echo "  kvm-migrator user already exists"
fi

# Polkit rule for libvirt access.
cat > /etc/polkit-1/rules.d/50-kvm-migrator.rules << 'POLKIT_EOF'
polkit.addRule(function(action, subject) {
    if (action.id.indexOf("org.libvirt.") === 0 &&
        subject.user === "kvm-migrator") {
        return polkit.Result.YES;
    }
});
POLKIT_EOF

# SSH keypair for migration trust.
MIGRATOR_SSH="/var/lib/kvm-migrator/.ssh"
mkdir -p "$MIGRATOR_SSH"
if [[ ! -f "${MIGRATOR_SSH}/id_ed25519" ]]; then
  # Check for a shared key provided alongside this installer.
  KEY_SRC="${SCRIPT_DIR}/kvm-migrator-key"
  PUBKEY_SRC="${SCRIPT_DIR}/kvm-migrator-key.pub"
  if [[ -f "$KEY_SRC" && -f "$PUBKEY_SRC" ]]; then
    cp "$KEY_SRC" "${MIGRATOR_SSH}/id_ed25519"
    cp "$PUBKEY_SRC" "${MIGRATOR_SSH}/id_ed25519.pub"
    echo "  Installed shared migration keypair"
  else
    ssh-keygen -t ed25519 -N "" -f "${MIGRATOR_SSH}/id_ed25519" -C kvm-migrator >/dev/null 2>&1
    echo "  Generated new migration keypair (exchange with existing nodes manually)"
  fi
fi

# Authorize the key and populate known_hosts for other nodes.
PUBKEY=$(cat "${MIGRATOR_SSH}/id_ed25519.pub")
grep -qF "$PUBKEY" "${MIGRATOR_SSH}/authorized_keys" 2>/dev/null || echo "$PUBKEY" >> "${MIGRATOR_SSH}/authorized_keys"
chown -R kvm-migrator:kvm-migrator "$MIGRATOR_SSH"
chmod 700 "$MIGRATOR_SSH"
chmod 600 "${MIGRATOR_SSH}/id_ed25519" "${MIGRATOR_SSH}/authorized_keys"
echo "  Migration keypair ready"

# ── Enable and start ───────────────────────────────────────────────────────────
systemctl daemon-reload
systemctl enable "${SERVICE_NAME}"
systemctl restart "${SERVICE_NAME}"

echo ""
echo "Done! Agent service status:"
systemctl status "${SERVICE_NAME}" --no-pager -l || true
echo ""
echo "Logs: journalctl -u ${SERVICE_NAME} -f"
echo "      tail -f ${LOG_FILE}"
echo ""
echo "Migration: kvm-migrator user is ready. To enable migration to/from"
echo "this node, exchange the public key at ${MIGRATOR_SSH}/id_ed25519.pub"
echo "with all other hypervisors in the cluster."
