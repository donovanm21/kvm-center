#!/usr/bin/env bash
# install.sh — unified Linux installer for the kvm-center stack (v5.47.1+).
#
# One script, two outcomes:
#
#   - mode=agent → installs `kvm-center-agent` on a libvirt hypervisor
#                  (mTLS enrolment, qemu+tls migration setup, firewalld,
#                  kvm-migrator user, libvirt qemu hook).
#   - mode=tools → installs `kvm-tools` inside a guest VM (small daemon
#                  that reports OS / kernel / IP / FS to the server).
#
# Mode resolution (first match wins):
#   1. `--mode {agent,tools}` flag.
#   2. The script's invocation name — `install-agent.sh` → agent,
#      `install-kvm-tools.sh` → tools. Lets the historical /downloads/
#      URLs keep working as symlinks pointing at this file.
#   3. Auto-detect: libvirtd present + virsh present → agent, else tools.
#
# Copy-paste one-liner from the Deployment page in the SPA wires the
# right flags for you. Hypervisor flow needs --host-id + --enroll-token
# from Settings → TLS → Hypervisor PKI.
#
# Usage:
#   curl -fsSLk https://<server>:8190/downloads/install.sh | sudo bash -s -- \
#     --server https://<server>:8190 \
#     [--mode agent|tools] \
#     [--host-id hv01.example.com] [--enroll-token enroll_…] \
#     [--port 8081] [--interval 15s]
#
# TLS: every download uses curl -k to tolerate the self-signed cert
# the server ships with by default. Replace the cert via Settings → TLS
# once you have a real DNS name.

set -euo pipefail

# ── Defaults ──────────────────────────────────────────────────────────────────
SERVER_URL=""
MODE=""
HOST_ID=""
ENROLL_TOKEN=""
PKI_SERVER=""
AGENT_PORT=8081
INTERVAL=""              # mode-specific default below
UUID_OVERRIDE_FLAG=""    # tools-only escape hatch for cloned VMs

usage() {
  cat >&2 <<EOF
kvm-center unified installer

Usage:
  $0 --server <url> [flags]

Mode flags (or auto-detected from script name / libvirt presence):
  --mode agent           Install kvm-center-agent (hypervisor)
  --mode tools           Install kvm-tools (guest VM)

Required:
  --server <url>         Control plane URL, e.g. https://kvm-center.example.com:8190

Agent-only:
  --host-id <id>         Host ID (default: hostname -s). Must match the ID the
                         enrollment token was issued for.
  --enroll-token <tok>   One-time mTLS enrollment token from Settings → TLS.
  --pki-server <url>     Override the URL used for /api/ca/sign (default: --server).
  --port <port>          Agent command port (default: 8081).

Common:
  --interval <duration>  Telemetry interval (default: agent=15s, tools=30s).

Tools-only:
  --uuid <uuid>          Pin a UUID override at /var/lib/kvm-tools/uuid (cloned-VM
                         escape hatch — usually unnecessary).

Examples:
  # Hypervisor enrollment
  curl -fsSLk https://kvm-center.example.com:8190/downloads/install.sh | sudo bash -s -- \\
    --server https://kvm-center.example.com:8190 \\
    --host-id hv01.example.com --enroll-token enroll_…

  # Guest tools install
  curl -fsSLk https://kvm-center.example.com:8190/downloads/install.sh | sudo bash -s -- \\
    --server https://kvm-center.example.com:8190
EOF
  exit "${1:-1}"
}

# ── Parse arguments ───────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode)         MODE="$2"; shift 2 ;;
    --server)       SERVER_URL="$2"; shift 2 ;;
    --port)         AGENT_PORT="$2"; shift 2 ;;
    --interval)     INTERVAL="$2";  shift 2 ;;
    --enroll-token) ENROLL_TOKEN="$2"; shift 2 ;;
    --pki-server)   PKI_SERVER="$2"; shift 2 ;;
    --host-id)      HOST_ID="$2"; shift 2 ;;
    --uuid)         UUID_OVERRIDE_FLAG="$2"; shift 2 ;;
    --help|-h)      usage 0 ;;
    *) echo "Unknown argument: $1" >&2; usage ;;
  esac
done

# Tolerate KVM_TOOLS_SERVER for the legacy guest-tools env-var contract.
[[ -z "$SERVER_URL" && -n "${KVM_TOOLS_SERVER:-}" ]] && SERVER_URL="$KVM_TOOLS_SERVER"
[[ -z "$SERVER_URL" ]] && { echo "ERROR: --server is required" >&2; usage; }

# ── Mode resolution ───────────────────────────────────────────────────────────
# 1. Explicit flag (already set above via $MODE).
# 2. Script invocation name — supports the back-compat aliases.
# 3. Auto-detect from libvirt presence.
if [[ -z "$MODE" ]]; then
  invocation="$(basename "${0:-install.sh}" 2>/dev/null || echo install.sh)"
  case "$invocation" in
    install-agent.sh)     MODE="agent" ;;
    install-kvm-tools.sh) MODE="tools" ;;
  esac
fi
if [[ -z "$MODE" ]]; then
  if command -v virsh >/dev/null 2>&1 && systemctl list-unit-files libvirtd.service >/dev/null 2>&1; then
    MODE="agent"
  else
    MODE="tools"
  fi
fi
case "$MODE" in
  agent|tools) ;;
  *) echo "ERROR: --mode must be one of: agent, tools (got '$MODE')" >&2; exit 1 ;;
esac

# ── Common preflight ──────────────────────────────────────────────────────────
if [[ $EUID -ne 0 ]]; then
  echo "ERROR: must run as root (use sudo)" >&2
  exit 1
fi

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)  ARCH_SUFFIX="amd64" ;;
  aarch64|arm64) ARCH_SUFFIX="arm64" ;;
  *) echo "ERROR: unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

# When the script was fetched via `curl | bash`, BASH_SOURCE[0] is unset.
# Treat that as "no local dir" and fall through to the download step.
if [[ -n "${BASH_SOURCE[0]:-}" ]]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
else
  SCRIPT_DIR=""
fi

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
# All progress / status output goes to stderr so it never bleeds into
# `$(…)` captures further down — `fetch_or_local` uses `echo "$path"` on
# stdout to return its result, and an `info "Downloading…"` call inside
# the same function used to land on stdout too, prepending colored text
# to the returned path and breaking the subsequent `cp "$binary" …`.
info()    { echo -e "${GREEN}✓${NC} $1" >&2; }
warn()    { echo -e "${YELLOW}⚠${NC} $1" >&2; }
fail()    { echo -e "${RED}✗${NC} $1" >&2; exit 1; }

# fetch_or_local <local-name> <download-name>
# Looks for the binary next to the script first; falls back to /downloads/
# on the control plane.
fetch_or_local() {
  local local_name="$1"
  local dl_name="$2"
  local out=""
  for candidate in \
      "${SCRIPT_DIR:+${SCRIPT_DIR}/${local_name}}" \
      "${SCRIPT_DIR:+${SCRIPT_DIR}/${local_name%-*}}" \
      "./${local_name}" \
      "./${local_name%-*}"; do
    if [[ -n "$candidate" && -f "$candidate" ]]; then
      out="$candidate"
      break
    fi
  done
  if [[ -z "$out" ]]; then
    local url="${SERVER_URL%/}/downloads/${dl_name}"
    out="/tmp/${dl_name}"
    info "Downloading $dl_name from $url"
    curl -fsSLk "$url" -o "$out" || fail "could not fetch $url"
    chmod +x "$out"
  fi
  echo "$out"
}

# ─────────────────────────────────────────────────────────────────────────────
# AGENT MODE
# ─────────────────────────────────────────────────────────────────────────────
install_agent() {
  local install_dir="/opt/kvm-center"
  local binary_name="kvm-center-agent"
  local service_name="kvm-center-agent"
  local log_file="/var/log/kvm-center-agent.log"
  local tls_dir="/etc/kvm-center/tls"

  [[ -z "$HOST_ID" ]] && HOST_ID="$(hostname -s)"
  [[ -z "$PKI_SERVER" ]] && PKI_SERVER="$SERVER_URL"
  [[ -z "$INTERVAL" ]] && INTERVAL="15s"

  echo
  info "Mode:    agent"
  info "Server:  $SERVER_URL"
  info "Host ID: $HOST_ID"
  info "Port:    $AGENT_PORT"
  info "Arch:    $ARCH_SUFFIX"
  echo

  local binary
  binary="$(fetch_or_local "${binary_name}-${ARCH_SUFFIX}" "${binary_name}-${ARCH_SUFFIX}")"

  mkdir -p "$install_dir"
  local target="${install_dir}/${binary_name}"
  cp "$binary" "${target}.new"
  chown root:root "${target}.new"
  chmod 0755 "${target}.new"
  mv -f "${target}.new" "$target"
  command -v restorecon >/dev/null 2>&1 && restorecon -F "$target" || true
  info "Binary installed at $target"

  # Systemd unit.
  cat > "/etc/systemd/system/${service_name}.service" <<EOF
[Unit]
Description=KVM Center Agent
After=network.target libvirtd.service
Wants=libvirtd.service

[Service]
Type=simple
ExecStart=${target} -server ${SERVER_URL} -port ${AGENT_PORT} -interval ${INTERVAL}
Restart=always
RestartSec=10
StandardOutput=append:${log_file}
StandardError=append:${log_file}
SupplementaryGroups=libvirt kvm

[Install]
WantedBy=multi-user.target
EOF

  # ── kvm-migrator user for live migration ────────────────────────────────
  info "Setting up kvm-migrator user for VM live migration"
  if ! id kvm-migrator &>/dev/null; then
    useradd -r -m -d /var/lib/kvm-migrator -s /bin/bash -G libvirt,kvm,qemu kvm-migrator
  else
    usermod -aG libvirt,kvm,qemu kvm-migrator 2>/dev/null
  fi

  chown root:qemu /var/lib/libvirt/images 2>/dev/null
  chmod 775 /var/lib/libvirt/images 2>/dev/null

  for bconf in /etc/qemu-kvm/bridge.conf /etc/qemu/bridge.conf; do
    [[ -d "$(dirname "$bconf")" ]] || continue
    grep -q "allow br0" "$bconf" 2>/dev/null || echo "allow br0" >> "$bconf"
  done

  cat > /etc/polkit-1/rules.d/50-kvm-migrator.rules <<'POLKIT_EOF'
polkit.addRule(function(action, subject) {
    if (action.id.indexOf("org.libvirt.") === 0 &&
        subject.user === "kvm-migrator") {
        return polkit.Result.YES;
    }
});
POLKIT_EOF

  local migrator_ssh="/var/lib/kvm-migrator/.ssh"
  mkdir -p "$migrator_ssh"
  if [[ ! -f "${migrator_ssh}/id_ed25519" ]]; then
    ssh-keygen -t ed25519 -N "" -f "${migrator_ssh}/id_ed25519" -C kvm-migrator >/dev/null 2>&1
  fi
  local pubkey
  pubkey="$(cat "${migrator_ssh}/id_ed25519.pub")"
  grep -qF "$pubkey" "${migrator_ssh}/authorized_keys" 2>/dev/null || echo "$pubkey" >> "${migrator_ssh}/authorized_keys"

  # Pull operator-curated known_hosts so SSHStrictHostKeyChecking works without
  # manual fleet edits. Best-effort.
  local known_hosts_body
  known_hosts_body="$(curl -fsSLk --max-time 10 "${SERVER_URL%/}/api/agents/ssh-known-hosts" 2>/dev/null || true)"
  if [[ -n "$known_hosts_body" ]]; then
    echo "$known_hosts_body" > "${migrator_ssh}/known_hosts"
    info "Pulled curated known_hosts from server"
  fi

  chown -R kvm-migrator:kvm-migrator "$migrator_ssh"
  chmod 700 "$migrator_ssh"
  chmod 600 "${migrator_ssh}/id_ed25519" "${migrator_ssh}/authorized_keys"
  [[ -f "${migrator_ssh}/known_hosts" ]] && chmod 644 "${migrator_ssh}/known_hosts"

  # ── Hypervisor PKI enrolment (mTLS) ─────────────────────────────────────
  if [[ -n "$ENROLL_TOKEN" ]]; then
    command -v openssl >/dev/null 2>&1 || fail "openssl is required for --enroll-token"
    mkdir -p "$tls_dir"
    chmod 700 "$tls_dir"
    local key_path="$tls_dir/agent.key"
    local csr_path="$tls_dir/agent.csr"
    local cert_path="$tls_dir/agent.crt"
    local ca_path="$tls_dir/ca.crt"

    info "Generating agent keypair + CSR for host ID '$HOST_ID'"
    openssl ecparam -name prime256v1 -genkey -noout -out "$key_path" 2>/dev/null
    chmod 600 "$key_path"
    openssl req -new -key "$key_path" -subj "/CN=${HOST_ID}" -out "$csr_path" 2>/dev/null

    local csr_pem csr_esc body resp
    csr_pem="$(cat "$csr_path")"
    csr_esc="$(printf '%s' "$csr_pem" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')"
    body="{\"enroll_token\":\"${ENROLL_TOKEN}\",\"csr\":${csr_esc}}"

    info "Exchanging CSR with ${PKI_SERVER}/api/ca/sign"
    resp="$(curl -fsS -k -H "Content-Type: application/json" -d "$body" "${PKI_SERVER%/}/api/ca/sign")" \
      || fail "CSR sign request failed — is the enrolment token still valid?"

    python3 - <<PYEOF
import json
d = json.loads('''${resp}''')
open("${cert_path}","w").write(d["cert"])
open("${ca_path}","w").write(d["ca_cert"])
print(f"  Issued cert valid until {d.get('not_after','?')}")
PYEOF
    chmod 644 "$cert_path" "$ca_path"
    info "PKI bundle ready: $cert_path / $key_path / $ca_path"

    # libvirt qemu+tls bootstrap.
    if command -v virsh >/dev/null 2>&1; then
      install -d -m 755 /etc/pki/CA /etc/pki/libvirt /etc/pki/libvirt/private
      install -m 644 "$ca_path"   /etc/pki/CA/cacert.pem
      install -m 644 "$cert_path" /etc/pki/libvirt/servercert.pem
      install -m 644 "$cert_path" /etc/pki/libvirt/clientcert.pem
      install -m 600 "$key_path"  /etc/pki/libvirt/private/serverkey.pem
      install -m 600 "$key_path"  /etc/pki/libvirt/private/clientkey.pem

      local lvconf=/etc/libvirt/libvirtd.conf
      if [[ -f "$lvconf" ]]; then
        for pair in 'listen_tls=1' 'listen_tcp=0' 'tls_no_verify_certificate=0'; do
          local key="${pair%%=*}"
          if grep -qE "^${key}\s*=" "$lvconf"; then
            sed -i "s|^${key}\s*=.*|${pair}|" "$lvconf"
          else
            echo "$pair" >> "$lvconf"
          fi
        done
      fi

      rm -f /etc/systemd/system/libvirtd.service.d/10-kvm-center-listen.conf
      rmdir /etc/systemd/system/libvirtd.service.d 2>/dev/null || true

      systemctl daemon-reload
      systemctl stop    libvirtd-tcp.socket 2>/dev/null || true
      systemctl disable libvirtd-tcp.socket 2>/dev/null || true
      systemctl enable  --now libvirtd-tls.socket 2>/dev/null || true
      systemctl restart libvirtd || warn "libvirtd restart failed — review manually"
      info "libvirtd-tls.socket active on :16514 (qemu+tls migrations enabled)"

      # Migration-grant qemu hook.
      local hook_src=""
      if [[ -n "${SCRIPT_DIR:-}" && -f "${SCRIPT_DIR}/libvirt-hooks-qemu.sh" ]]; then
        hook_src="${SCRIPT_DIR}/libvirt-hooks-qemu.sh"
      else
        hook_src="$(mktemp)"
        curl -fsSLk "${SERVER_URL%/}/downloads/libvirt-hooks-qemu.sh" -o "$hook_src" 2>/dev/null || hook_src=""
      fi
      if [[ -n "$hook_src" && -f "$hook_src" ]]; then
        install -d -m 755 /etc/libvirt/hooks
        install -m 755 "$hook_src" /etc/libvirt/hooks/qemu
        systemctl reload libvirtd 2>/dev/null || systemctl kill -s HUP libvirtd 2>/dev/null || true
        info "Installed migration-grant hook at /etc/libvirt/hooks/qemu"
      fi

      # Firewalld: open the agent command port + libvirt TLS + qemu migration range.
      if systemctl is-active --quiet firewalld; then
        firewall-cmd --permanent --add-port="${AGENT_PORT}/tcp"  >/dev/null 2>&1 || true
        firewall-cmd --permanent --add-port=16514/tcp            >/dev/null 2>&1 || true
        firewall-cmd --permanent --add-port=49152-49215/tcp      >/dev/null 2>&1 || true
        firewall-cmd --permanent --add-service=libvirt-tls       >/dev/null 2>&1 || true
        firewall-cmd --reload                                    >/dev/null 2>&1 || true
        info "Firewalld: ${AGENT_PORT}/tcp + 16514/tcp + 49152-49215/tcp + libvirt-tls"
      else
        warn "firewalld inactive — ensure ${AGENT_PORT}/tcp + 16514/tcp + 49152-49215/tcp are reachable"
      fi
    fi
  else
    warn "No --enroll-token supplied; skipping mTLS setup. Migrations + commands "
    warn "from kvm-center will fail until you re-run with a token from Settings → TLS."
  fi

  systemctl daemon-reload
  systemctl enable "$service_name"
  systemctl restart "$service_name"
  echo
  systemctl status "$service_name" --no-pager -l || true
  echo
  info "Logs: journalctl -u $service_name -f"
}

# ─────────────────────────────────────────────────────────────────────────────
# TOOLS MODE
# ─────────────────────────────────────────────────────────────────────────────
install_tools() {
  local install_dir="/opt/kvm-tools"
  local bin_name="kvm-tools"
  local service_name="kvm-tools"

  [[ -z "$INTERVAL" ]] && INTERVAL="30s"

  echo
  info "Mode:     tools"
  info "Server:   $SERVER_URL"
  info "Interval: $INTERVAL"
  info "Arch:     $ARCH_SUFFIX"
  echo

  local binary
  binary="$(fetch_or_local "${bin_name}-${ARCH_SUFFIX}" "${bin_name}-linux-${ARCH_SUFFIX}")"

  mkdir -p "$install_dir"
  if systemctl is-active --quiet "$service_name" 2>/dev/null; then
    info "Stopping existing $service_name for upgrade"
    systemctl stop "$service_name"
  fi
  cp "$binary" "$install_dir/$bin_name"
  chmod +x "$install_dir/$bin_name"
  info "Binary installed to $install_dir/$bin_name"

  # DMI UUID sanity.
  if [[ -r /sys/class/dmi/id/product_uuid ]]; then
    info "Detected DMI UUID: $(cat /sys/class/dmi/id/product_uuid)"
  else
    warn "Cannot read /sys/class/dmi/id/product_uuid — kvm-tools may fail to start"
  fi

  # UUID override (cloned-VM escape hatch). Honours both --uuid and the
  # legacy UUID_OVERRIDE env-var.
  local uuid_override="${UUID_OVERRIDE_FLAG:-${UUID_OVERRIDE:-}}"
  if [[ -n "$uuid_override" ]]; then
    mkdir -p /var/lib/kvm-tools
    echo "$uuid_override" > /var/lib/kvm-tools/uuid
    chmod 0644 /var/lib/kvm-tools/uuid
    info "Pinned UUID override: $uuid_override → /var/lib/kvm-tools/uuid"
  fi

  cat > "/etc/systemd/system/${service_name}.service" <<EOF
[Unit]
Description=KVM Center Guest Tools
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$install_dir/$bin_name -server $SERVER_URL -interval $INTERVAL
Restart=always
RestartSec=10
StandardOutput=append:/var/log/kvm-tools.log
StandardError=append:/var/log/kvm-tools.log

[Install]
WantedBy=multi-user.target
EOF
  info "Systemd unit written"

  systemctl daemon-reload
  systemctl enable "$service_name" >/dev/null 2>&1
  systemctl restart "$service_name"
  sleep 1

  if systemctl is-active --quiet "$service_name"; then
    info "Service $service_name running"
    info "Logs: journalctl -u $service_name -f"
  else
    fail "Service $service_name failed to start — check: journalctl -u $service_name -n 30"
  fi
}

case "$MODE" in
  agent) install_agent ;;
  tools) install_tools ;;
esac
