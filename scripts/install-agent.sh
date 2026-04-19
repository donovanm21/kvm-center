#!/usr/bin/env bash
# install-agent.sh — Install and configure the kvm-center agent as a systemd service.
# Run as root on each KVM hypervisor.
#
# Usage:
#   sudo ./install-agent.sh --server https://<server-ip>:8190 [--port 8081] [--interval 15s]
#
# TLS note: the agent trusts self-signed certs by default and logs a
# warning each report. Install a trusted cert via Settings → TLS.
#
set -euo pipefail

INSTALL_DIR="/opt/kvm-center"
BINARY_NAME="kvm-center-agent"
SERVICE_NAME="kvm-center-agent"
LOG_FILE="/var/log/kvm-center-agent.log"

# Defaults
SERVER_URL=""
AGENT_PORT=8081
INTERVAL="15s"
ARCH="$(uname -m)"
ENROLL_TOKEN=""
TLS_DIR="/etc/kvm-center/tls"
HOST_ID=""
PKI_SERVER=""

usage() {
  echo "Usage: $0 --server <url> [--port <port>] [--interval <duration>]"
  echo "                      [--enroll-token <token>] [--pki-server <url>] [--host-id <id>]"
  echo "  --server        Control plane URL (required), e.g. https://kvm-center.example.com:8190"
  echo "  --port          Agent command port (default: 8081)"
  echo "  --interval      Telemetry interval (default: 15s)"
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
# When the script was fetched via `curl | bash`, BASH_SOURCE[0] is
# unset — set -u then barfs. Treat that as "no local dir" and fall
# through to the download step.
if [[ -n "${BASH_SOURCE[0]:-}" ]]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
else
  SCRIPT_DIR=""
fi
BINARY=""
for candidate in \
  "${SCRIPT_DIR:+${SCRIPT_DIR}/${BINARY_NAME}-${ARCH_SUFFIX}}" \
  "${SCRIPT_DIR:+${SCRIPT_DIR}/${BINARY_NAME}}" \
  "./${BINARY_NAME}-${ARCH_SUFFIX}" \
  "./${BINARY_NAME}"; do
  if [[ -n "$candidate" && -f "$candidate" ]]; then
    BINARY="$candidate"
    break
  fi
done

# curl|bash path: no local binary beside the script, so pull it from
# /downloads/ on the control plane. -k tolerates the default
# self-signed cert, matching the install snippet the SPA generates.
if [[ -z "$BINARY" ]]; then
  DL_URL="${SERVER_URL%/}/downloads/${BINARY_NAME}-${ARCH_SUFFIX}"
  BINARY="/tmp/${BINARY_NAME}-${ARCH_SUFFIX}"
  echo "Downloading agent binary: $DL_URL"
  if ! curl -fsSLk "$DL_URL" -o "$BINARY"; then
    echo "ERROR: Could not fetch agent binary from $DL_URL"
    echo "Verify the control plane is reachable and /downloads/ is serving arch $ARCH_SUFFIX."
    exit 1
  fi
  chmod +x "$BINARY"
fi

echo "Installing kvm-center agent..."
echo "  Binary   : $BINARY (arch: $ARCH_SUFFIX)"
echo "  Server   : $SERVER_URL"
echo "  Port     : $AGENT_PORT"
echo "  Interval : $INTERVAL"
echo ""

# ── Install ────────────────────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"
# Use atomic rename so re-running this script against a host where the
# agent is already executing doesn't fail with "Text file busy" — the
# kernel tolerates unlink + replace of a running mapped binary.
TARGET="${INSTALL_DIR}/${BINARY_NAME}"
TMP_TARGET="${TARGET}.new"
cp "$BINARY" "$TMP_TARGET"
chown root:root "$TMP_TARGET"
chmod 0755 "$TMP_TARGET"
mv -f "$TMP_TARGET" "$TARGET"
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

# SSH keypair for the legacy qemu+ssh migration fallback. Always
# generated locally per host (v5.20.9 — the shared repo-committed
# keypair was retired after it leaked through gitleaks). mTLS /
# qemu+tls migrations are the default on v5.10+ and don't need this
# key; it only matters for the legacy SSH fallback between nodes that
# haven't been mTLS-enrolled.
MIGRATOR_SSH="/var/lib/kvm-migrator/.ssh"
mkdir -p "$MIGRATOR_SSH"
if [[ ! -f "${MIGRATOR_SSH}/id_ed25519" ]]; then
  ssh-keygen -t ed25519 -N "" -f "${MIGRATOR_SSH}/id_ed25519" -C kvm-migrator >/dev/null 2>&1
  echo "  Generated migration keypair (cross-authorize with peers if you use the SSH fallback)"
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

  # ── libvirt qemu+tls:// configuration (v5.10.1+) ──────────────────────
  # Install the issued bundle to the paths libvirtd reads so migrations
  # can run over qemu+tls:// with mutual cert auth. Every hypervisor
  # enrolls against the same cluster CA, so their client + server certs
  # chain to the same root.
  if command -v virsh >/dev/null 2>&1; then
    echo "  Installing cert bundle for libvirt…"
    install -d -m 755 /etc/pki/CA /etc/pki/libvirt /etc/pki/libvirt/private
    install -m 644 "$CA_PATH"   /etc/pki/CA/cacert.pem
    # libvirtd uses the same cert for server + client auth in mutual-TLS
    # mode — the issued leaf has both ExtKeyUsage bits set.
    install -m 644 "$CERT_PATH" /etc/pki/libvirt/servercert.pem
    install -m 644 "$CERT_PATH" /etc/pki/libvirt/clientcert.pem
    install -m 600 "$KEY_PATH"  /etc/pki/libvirt/private/serverkey.pem
    install -m 600 "$KEY_PATH"  /etc/pki/libvirt/private/clientkey.pem

    # libvirtd.conf — keep the legacy knobs in case a peer still speaks
    # qemu+tcp, but the actual listener is driven by systemd sockets.
    LVCONF=/etc/libvirt/libvirtd.conf
    if [[ -f "$LVCONF" ]]; then
      for pair in 'listen_tls=1' 'listen_tcp=0' 'tls_no_verify_certificate=0'; do
        key="${pair%%=*}"
        if grep -qE "^${key}\s*=" "$LVCONF"; then
          sed -i "s|^${key}\s*=.*|${pair}|" "$LVCONF"
        else
          echo "$pair" >> "$LVCONF"
        fi
      done
    fi

    # Clean up any legacy --listen drop-in that pre-v5.10.1 installs
    # may have planted — incompatible with socket-activated libvirtd
    # on current RHEL / Rocky 9+.
    rm -f /etc/systemd/system/libvirtd.service.d/10-kvm-center-listen.conf
    rmdir /etc/systemd/system/libvirtd.service.d 2>/dev/null || true

    # Enable the socket units instead of --listen. libvirtd-tls.socket
    # listens on :16514; the plain TCP socket is explicitly disabled.
    systemctl daemon-reload
    systemctl stop    libvirtd-tcp.socket 2>/dev/null || true
    systemctl disable libvirtd-tcp.socket 2>/dev/null || true
    systemctl enable  --now libvirtd-tls.socket 2>/dev/null || true
    systemctl restart libvirtd || echo "  WARNING: libvirtd restart failed — review manually"
    echo "  libvirtd-tls.socket active on :16514 (qemu+tls:// migrations enabled)"

    # libvirt qemu hook (v5.11.1) — enforces that incoming migrations
    # were authorised by kvm-center. Installed idempotently; the hook
    # fails open if the agent is unreachable so migrations aren't
    # gated on agent liveness.
    HOOK_SRC="${SCRIPT_DIR:+${SCRIPT_DIR}/libvirt-hooks-qemu.sh}"
    HOOK_DST="/etc/libvirt/hooks/qemu"
    # Fall back to the build portal when the hook isn't colocated with
    # this installer (curl | bash style).
    if [[ ! -f "$HOOK_SRC" ]]; then
      # v5.16+: downloads live on the server itself under /downloads/.
      TMP_HOOK="$(mktemp)"
      if curl -fsSLk "${SERVER_URL%/}/downloads/libvirt-hooks-qemu.sh" -o "$TMP_HOOK" 2>/dev/null; then
        HOOK_SRC="$TMP_HOOK"
      fi
    fi
    if [[ -f "$HOOK_SRC" ]]; then
      install -d -m 755 /etc/libvirt/hooks
      install -m 755 "$HOOK_SRC" "$HOOK_DST"
      # libvirtd reloads hooks on SIGHUP without dropping connections.
      systemctl reload libvirtd 2>/dev/null || systemctl kill -s HUP libvirtd 2>/dev/null || true
      echo "  Installed migration-grant hook at $HOOK_DST"
    fi

    # Firewall — open the TLS control port + QEMU migration data range.
    # No-op when firewalld isn't running. iptables / nftables users
    # should open these manually.
    if systemctl is-active --quiet firewalld; then
      firewall-cmd --permanent --add-port=16514/tcp         >/dev/null 2>&1 || true
      firewall-cmd --permanent --add-port=49152-49215/tcp   >/dev/null 2>&1 || true
      firewall-cmd --reload                                 >/dev/null 2>&1 || true
      echo "  Firewall opened: 16514/tcp (TLS control), 49152-49215/tcp (migration data)"
    else
      echo "  firewalld inactive — ensure 16514/tcp + 49152-49215/tcp are reachable for migrations"
    fi
  fi
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
