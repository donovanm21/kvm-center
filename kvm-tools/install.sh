#!/bin/bash
# install.sh — one-shot installer for kvm-tools on a guest VM.
#
# Installs /opt/kvm-tools/kvm-tools and a systemd unit that reports guest
# telemetry (OS, kernel, IPs, filesystems) to the kvm-center server.
#
# Usage:
#   sudo ./install.sh --server http://REDACTED-HOST:8090 [--interval 30s]
#
# Or set via env: KVM_TOOLS_SERVER=http://... sudo ./install.sh

set -euo pipefail

SERVER="${KVM_TOOLS_SERVER:-http://REDACTED-HOST:8090}"
INTERVAL="30s"
INSTALL_DIR="/opt/kvm-tools"
BIN_NAME="kvm-tools"
SERVICE_NAME="kvm-tools"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}✓${NC} $1"; }
warn()  { echo -e "${YELLOW}⚠${NC} $1"; }
fail()  { echo -e "${RED}✗${NC} $1" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server)   SERVER="$2"; shift 2 ;;
    --interval) INTERVAL="$2"; shift 2 ;;
    -h|--help)
      cat <<EOF
kvm-tools installer

Options:
  --server <url>       kvm-center server URL (default: $SERVER)
  --interval <dur>     Report interval (default: $INTERVAL)
EOF
      exit 0 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done

[[ $EUID -eq 0 ]] || fail "Must run as root (use sudo)"

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) BIN_SRC_CANDIDATES=("$SCRIPT_DIR/kvm-tools-amd64" "$SCRIPT_DIR/kvm-tools") ;;
  aarch64|arm64) BIN_SRC_CANDIDATES=("$SCRIPT_DIR/kvm-tools-arm64" "$SCRIPT_DIR/kvm-tools") ;;
  *) fail "Unsupported architecture: $ARCH" ;;
esac

BIN_SRC=""
for c in "${BIN_SRC_CANDIDATES[@]}"; do
  if [[ -f "$c" ]]; then BIN_SRC="$c"; break; fi
done
[[ -n "$BIN_SRC" ]] || fail "Could not find kvm-tools binary next to this script (looked for ${BIN_SRC_CANDIDATES[*]})"

info "Installing from: $BIN_SRC"
mkdir -p "$INSTALL_DIR"
# Stop existing service first so we can overwrite the running binary.
if systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; then
  info "Stopping existing $SERVICE_NAME for upgrade"
  systemctl stop "$SERVICE_NAME"
fi
cp "$BIN_SRC" "$INSTALL_DIR/$BIN_NAME"
chmod +x "$INSTALL_DIR/$BIN_NAME"
info "Binary installed to $INSTALL_DIR/$BIN_NAME"

# Verify DMI UUID is readable (matches libvirt domain UUID on non-cloned VMs).
if [[ -r /sys/class/dmi/id/product_uuid ]]; then
  UUID="$(cat /sys/class/dmi/id/product_uuid)"
  info "Detected DMI UUID: $UUID"
else
  warn "Cannot read /sys/class/dmi/id/product_uuid — kvm-tools may fail to start"
fi

# --uuid override: seed the persistent cache file that kvm-tools reads
# on startup (preferred over DMI) — escape hatch for cloned VMs whose
# SMBIOS UUID matches a different libvirt domain. The server-side
# canonical_uuid resolution usually handles this automatically on the
# first telemetry tick, but operators can bake the correct UUID in up
# front too.
if [[ -n "${UUID_OVERRIDE:-}" ]]; then
  mkdir -p /var/lib/kvm-tools
  echo "$UUID_OVERRIDE" > /var/lib/kvm-tools/uuid
  chmod 0644 /var/lib/kvm-tools/uuid
  info "Pinned UUID override: $UUID_OVERRIDE → /var/lib/kvm-tools/uuid"
fi

cat > /etc/systemd/system/${SERVICE_NAME}.service <<EOF
[Unit]
Description=KVM Center Guest Tools
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$INSTALL_DIR/$BIN_NAME -server $SERVER -interval $INTERVAL
Restart=always
RestartSec=10
StandardOutput=append:/var/log/kvm-tools.log
StandardError=append:/var/log/kvm-tools.log

[Install]
WantedBy=multi-user.target
EOF
info "Systemd unit written to /etc/systemd/system/${SERVICE_NAME}.service"

systemctl daemon-reload
systemctl enable "$SERVICE_NAME" >/dev/null 2>&1
systemctl restart "$SERVICE_NAME"
sleep 1

if systemctl is-active --quiet "$SERVICE_NAME"; then
  info "Service $SERVICE_NAME is running"
  info "Live logs: journalctl -u $SERVICE_NAME -f"
else
  warn "Service $SERVICE_NAME failed to start — check: journalctl -u $SERVICE_NAME -n 30"
  exit 1
fi
