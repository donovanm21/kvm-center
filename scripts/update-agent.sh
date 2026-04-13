#!/usr/bin/env bash
# update-agent.sh — In-place update of an already-installed kvm-center agent.
#
# Designed to be safe to re-run on every deploy. Picks up the latest
# binary from the build portal, swaps it into /opt/kvm-center, restores
# the SELinux context (the /tmp staging step gets relabeled to
# user_tmp_t which systemd refuses to exec — restorecon fixes that),
# restarts the systemd unit, and smoke-tests the /migrate-status
# endpoint to confirm the new binary is alive.
#
# Usage (run as root on the hypervisor):
#   curl -fsSL http://<build-host>:<port>/update-agent.sh | bash
#
# Or with explicit URL override (handy when the script is bundled
# alongside binaries in a tarball):
#   ./update-agent.sh --from http://10.10.100.8:8097
#
set -euo pipefail

INSTALL_DIR="/opt/kvm-center"
BINARY_NAME="kvm-center-agent"
SERVICE_NAME="kvm-center-agent"
AGENT_PORT="${AGENT_PORT:-8081}"
BUILD_URL="${KVM_CENTER_BUILD_URL:-http://10.10.100.8:8097}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --from) BUILD_URL="$2"; shift 2 ;;
    --port) AGENT_PORT="$2"; shift 2 ;;
    --help|-h)
      echo "Usage: $0 [--from <build-url>] [--port <agent-port>]"
      echo "  --from   Build portal base URL (default: \$KVM_CENTER_BUILD_URL or http://10.10.100.8:8097)"
      echo "  --port   Local agent port for smoke test (default: 8081)"
      exit 0
      ;;
    *) echo "Unknown argument: $1"; exit 1 ;;
  esac
done

if [[ $EUID -ne 0 ]]; then
  echo "ERROR: must run as root" >&2
  exit 1
fi

if [[ ! -f "$INSTALL_DIR/$BINARY_NAME" ]]; then
  echo "ERROR: agent is not installed at $INSTALL_DIR/$BINARY_NAME" >&2
  echo "       run install-agent.sh --server <url> first" >&2
  exit 1
fi

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  ARCH_SUFFIX="amd64" ;;
  aarch64) ARCH_SUFFIX="arm64" ;;
  *) echo "ERROR: unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

URL="${BUILD_URL%/}/${BINARY_NAME}-${ARCH_SUFFIX}"
TMP="$(mktemp /tmp/kvm-center-agent.new.XXXXXX)"
trap 'rm -f "$TMP"' EXIT

echo "Fetching $URL"
if ! curl -fsSL -o "$TMP" "$URL"; then
  echo "ERROR: download failed from $URL" >&2
  echo "       set --from or KVM_CENTER_BUILD_URL to the correct build portal" >&2
  exit 1
fi
chmod 0755 "$TMP"

# Skip the swap if the binary on disk is byte-for-byte identical — saves
# an unnecessary service bounce.
if cmp -s "$TMP" "$INSTALL_DIR/$BINARY_NAME"; then
  echo "Already up to date — no change."
  exit 0
fi

echo "Swapping binary in place…"
systemctl stop "$SERVICE_NAME"
mv "$TMP" "$INSTALL_DIR/$BINARY_NAME"
chown root:root "$INSTALL_DIR/$BINARY_NAME"
chmod 0755 "$INSTALL_DIR/$BINARY_NAME"

# SELinux: a binary moved in from /tmp inherits user_tmp_t which systemd
# refuses to exec. restorecon resets to the parent dir's context (usr_t
# under default policy). Tolerate non-SELinux systems gracefully.
if command -v restorecon >/dev/null 2>&1; then
  restorecon -F "$INSTALL_DIR/$BINARY_NAME" || true
fi

systemctl start "$SERVICE_NAME"
sleep 2

if ! systemctl is-active --quiet "$SERVICE_NAME"; then
  echo "ERROR: agent failed to start — see: journalctl -u $SERVICE_NAME -n 30" >&2
  exit 1
fi

# Smoke test: every supported agent version exposes /migrate-status
# (returns the current MigrationStatus JSON, even when idle).
if curl -fsS -o /dev/null --max-time 5 "http://localhost:${AGENT_PORT}/migrate-status"; then
  echo "Agent updated and responding on :${AGENT_PORT}."
else
  echo "WARN: agent restarted but /migrate-status didn't respond — investigate" >&2
fi
