#!/usr/bin/env bash
# Explicit single-host binary update. Does not install packages, rewrite unit
# URLs, change libvirt settings or alter firewall rules. No fleet targeting.
set -euo pipefail
INSTALL_DIR=/opt/kvm-center
SERVICE=kvm-center-agent
BUILD_URL="${KVM_CENTER_BUILD_URL:-}"
AGENT_PORT=8081
CURL_TLS=(--proto '=https' --proto-redir '=https')
die() { echo "ERROR: $*" >&2; exit 1; }
while [[ $# -gt 0 ]]; do
  case "$1" in
    --from|--port|--install-dir)
      [[ $# -ge 2 && -n "$2" ]] || die "missing value for $1"
      case "$1" in --from) BUILD_URL="$2";; --port) AGENT_PORT="$2";; --install-dir) INSTALL_DIR="$2";; esac
      shift 2 ;;
    --insecure) CURL_TLS+=(-k); shift ;;
    -h|--help) echo "Usage: $0 --from https://SERVER/downloads [--port 8081] [--install-dir /opt/kvm-center] [--insecure]"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
[[ "$BUILD_URL" == https://* ]] || die "an explicit HTTPS --from URL is required"
[[ "$AGENT_PORT" =~ ^[0-9]+$ && ${#AGENT_PORT} -le 5 ]] || die "invalid port"
(( 10#$AGENT_PORT >= 1 && 10#$AGENT_PORT <= 65535 )) || die "invalid port"
[[ "$(id -u)" == 0 ]] || die "run as root on the explicitly selected host"
TARGET="$INSTALL_DIR/$SERVICE"
[[ -f "$TARGET" ]] || die "agent is not installed at $TARGET"
[[ ! -e "$TARGET.previous" ]] || die "preserve or resolve existing $TARGET.previous before updating"
case "$(uname -m)" in x86_64|amd64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) die "unsupported architecture";; esac
TMP=$(mktemp "$INSTALL_DIR/.agent-update.XXXXXX")
SWAPPED=0
STOPPED=0
cleanup() {
  rc=$?
  trap - EXIT
  rm -f "$TMP"
  if (( rc != 0 && STOPPED )); then
    if (( SWAPPED )); then
      systemctl stop "$SERVICE" || true
      mv -f "$TARGET.previous" "$TARGET" || { echo 'ERROR: rollback file restoration failed' >&2; exit 1; }
    fi
    systemctl start "$SERVICE" || echo 'ERROR: rollback service start failed' >&2
    echo 'Update failed; prior binary restored/restarted where possible. Inspect service health.' >&2
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
curl -fsSL "${CURL_TLS[@]}" --connect-timeout 10 --max-time 120 \
  "${BUILD_URL%/}/kvm-center-agent-$ARCH" -o "$TMP"
[[ -s "$TMP" ]] || die "empty download"
chmod 0755 "$TMP"
"$TMP" --version >/dev/null || die "candidate failed version check; existing agent preserved"
if cmp -s "$TMP" "$TARGET"; then echo 'Already up to date'; exit 0; fi
cp -p "$TARGET" "$TARGET.previous"
STOPPED=1
systemctl stop "$SERVICE"
SWAPPED=1
mv -f "$TMP" "$TARGET"
if command -v restorecon >/dev/null 2>&1; then restorecon -F "$TARGET"; fi
systemctl start "$SERVICE"
for _ in {1..10}; do
  if systemctl is-active --quiet "$SERVICE" && curl -fsS --connect-timeout 1 --max-time 2 "http://127.0.0.1:$AGENT_PORT/health" >/dev/null; then
    echo "Agent updated. Previous binary retained at $TARGET.previous"
    exit 0
  fi
  sleep 1
done
die "updated agent failed health check"
