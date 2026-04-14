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
ENROLL_TOKEN=""
TLS_DIR="/etc/kvm-center/tls"
HOST_ID=""
PKI_SERVER=""

usage() {
  echo "Usage: $0 --server <url> [--port <port>] [--interval <duration>]"
  echo "                      [--enroll-token <token>] [--pki-server <url>] [--host-id <id>]"
  echo "  --server        Control plane URL (required), e.g. http://10.10.100.8:8090"
  echo "  --port          Agent command port (default: 8081)"
  echo "  --interval      Telemetry interval (default: 30s)"
  echo "  --enroll-token  One-time token from kvm-center admin — exchanges a CSR for a"
  echo "                  signed leaf cert stored under $TLS_DIR (v5.10.0+)"
  echo "  --pki-server    Override the server URL for /api/ca/sign (defaults to --server)"
  echo "  --host-id       Host ID (default: hostname). Must match the ID used when the"
  echo "                  enrollment token was issued."
  exit 1
}

# ── Parse arguments ────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --server)       SERVER_URL="$2"; shift 2 ;;
    --port)         AGENT_PORT="$2"; shift 2 ;;
    --interval)     INTERVAL="$2";  shift 2 ;;
    --enroll-token) ENROLL_TOKEN="$2"; shift 2 ;;
    --pki-server)   PKI_SERVER="$2"; shift 2 ;;
    --host-id)      HOST_ID="$2"; shift 2 ;;
    --help|-h)      usage ;;
    *) echo "Unknown argument: $1"; usage ;;
  esac
done

[[ -z "$SERVER_URL" ]] && { echo "ERROR: --server is required"; usage; }
[[ -z "$HOST_ID" ]] && HOST_ID="$(hostname -s)"
[[ -z "$PKI_SERVER" ]] && PKI_SERVER="$SERVER_URL"

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
chown root:root "${INSTALL_DIR}/${BINARY_NAME}"
chmod 0755 "${INSTALL_DIR}/${BINARY_NAME}"
# SELinux: a binary copied in from /tmp or a curl-piped temp dir inherits
# user_tmp_t which systemd refuses to exec. Reset to the parent dir's
# context (usr_t under default policy). Tolerate non-SELinux systems.
if command -v restorecon >/dev/null 2>&1; then
  restorecon -F "${INSTALL_DIR}/${BINARY_NAME}" || true
fi

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
  useradd -r -m -d /var/lib/kvm-migrator -s /bin/bash -G libvirt,kvm,qemu kvm-migrator
  echo "  Created kvm-migrator user"
else
  usermod -aG libvirt,kvm,qemu kvm-migrator 2>/dev/null
  echo "  kvm-migrator user already exists"
fi

# Ensure kvm-migrator can write disk images during offline migration.
chown root:qemu /var/lib/libvirt/images 2>/dev/null
chmod 775 /var/lib/libvirt/images 2>/dev/null

# Allow qemu-bridge-helper to use br0 (needed for VMs migrated from other nodes).
# RHEL/Rocky use /etc/qemu-kvm/bridge.conf; Debian/Ubuntu use /etc/qemu/bridge.conf.
for bconf in /etc/qemu-kvm/bridge.conf /etc/qemu/bridge.conf; do
  dir="$(dirname "$bconf")"
  [ -d "$dir" ] || continue
  grep -q "allow br0" "$bconf" 2>/dev/null || echo "allow br0" >> "$bconf"
done

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

# ── Hypervisor PKI enrollment (v5.10.0) ────────────────────────────────────────
# When an --enroll-token is supplied, generate a private key + CSR and swap
# them with the kvm-center server for a signed leaf certificate. The bundle
# lands under $TLS_DIR for the upcoming mTLS migration channel. No-op when
# the token is blank so existing deploys keep working unchanged.
if [[ -n "$ENROLL_TOKEN" ]]; then
  if ! command -v openssl >/dev/null 2>&1; then
    echo "ERROR: openssl is required for --enroll-token flow"
    exit 1
  fi
  mkdir -p "$TLS_DIR"
  chmod 700 "$TLS_DIR"
  KEY_PATH="$TLS_DIR/agent.key"
  CSR_PATH="$TLS_DIR/agent.csr"
  CERT_PATH="$TLS_DIR/agent.crt"
  CA_PATH="$TLS_DIR/ca.crt"

  echo "  Generating agent keypair + CSR for host ID '$HOST_ID'…"
  openssl ecparam -name prime256v1 -genkey -noout -out "$KEY_PATH" 2>/dev/null
  chmod 600 "$KEY_PATH"
  openssl req -new -key "$KEY_PATH" -subj "/CN=${HOST_ID}" -out "$CSR_PATH" 2>/dev/null

  CSR_PEM=$(cat "$CSR_PATH")
  # Escape JSON (newlines → literal \n, quotes → \").
  CSR_ESC=$(printf '%s' "$CSR_PEM" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')
  BODY="{\"enroll_token\":\"${ENROLL_TOKEN}\",\"csr\":${CSR_ESC}}"

  echo "  Exchanging CSR with ${PKI_SERVER}/api/ca/sign…"
  RESP=$(curl -fsS -k -H "Content-Type: application/json" -d "$BODY" "${PKI_SERVER}/api/ca/sign") || {
    echo "ERROR: CSR sign request failed. Is the enrollment token still valid?"
    exit 1
  }
  echo "$RESP" | python3 -c '
import json, sys
d = json.loads(sys.stdin.read())
with open("'"$CERT_PATH"'","w") as f: f.write(d["cert"])
with open("'"$CA_PATH"'","w") as f: f.write(d["ca_cert"])
print("  Issued cert valid until", d.get("not_after","?"))
'
  chmod 644 "$CERT_PATH" "$CA_PATH"
  echo "  PKI bundle ready: $CERT_PATH, $KEY_PATH, $CA_PATH"
fi

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
