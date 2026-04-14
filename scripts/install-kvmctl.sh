#!/usr/bin/env bash
# Installs the kvmctl CLI. Detects OS + arch, fetches the matching
# binary from the kvm-center build portal, drops it into an install
# directory (default /usr/local/bin), and verifies.
#
# Usage:
#   curl -fsSL http://<portal>/install-kvmctl.sh | bash -s -- --portal http://<portal>
#   curl -fsSL http://<portal>/install-kvmctl.sh -o /tmp/i && bash /tmp/i --portal http://<portal> --install-dir ~/.local/bin
#
# Flags:
#   --portal      http://HOST:8097 base URL of the kvm-center build portal (required)
#   --install-dir target directory (default: /usr/local/bin, falls back to ~/.local/bin when /usr/local/bin isn't writable)
#   --bin-name    installed binary name (default: kvmctl)
#   --force       overwrite without prompting

set -euo pipefail

PORTAL=""
INSTALL_DIR=""
BIN_NAME="kvmctl"
FORCE=0

die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[36m>>\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m✓\033[0m  %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --portal)      PORTAL="$2"; shift 2 ;;
    --install-dir) INSTALL_DIR="$2"; shift 2 ;;
    --bin-name)    BIN_NAME="$2"; shift 2 ;;
    --force)       FORCE=1; shift ;;
    -h|--help)
      sed -n '2,20p' "$0"; exit 0 ;;
    *) die "unknown flag: $1" ;;
  esac
done

[[ -z "$PORTAL" ]] && die "--portal http://HOST:8097 is required"
PORTAL="${PORTAL%/}"

# ── Detect OS + arch ──────────────────────────────────────────────
uname_s="$(uname -s)"
uname_m="$(uname -m)"
case "$uname_s" in
  Linux)   os="linux" ;;
  Darwin)  os="darwin" ;;
  *)       die "unsupported OS: $uname_s (kvmctl supports linux, darwin, windows)" ;;
esac
case "$uname_m" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) die "unsupported arch: $uname_m" ;;
esac

ASSET="kvmctl-${os}-${arch}"
URL="${PORTAL}/${ASSET}"
info "detected: ${os}/${arch} → ${ASSET}"

# ── Pick install dir ──────────────────────────────────────────────
if [[ -z "$INSTALL_DIR" ]]; then
  if [[ -w "/usr/local/bin" ]] || [[ "$(id -u)" -eq 0 ]]; then
    INSTALL_DIR="/usr/local/bin"
  else
    INSTALL_DIR="$HOME/.local/bin"
  fi
fi
mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
TARGET="${INSTALL_DIR}/${BIN_NAME}"

# ── Elevation check ───────────────────────────────────────────────
SUDO=""
if [[ ! -w "$INSTALL_DIR" ]]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
    info "install dir ${INSTALL_DIR} not writable — will use sudo"
  else
    die "install dir ${INSTALL_DIR} not writable and sudo unavailable"
  fi
fi

# ── Overwrite guard ───────────────────────────────────────────────
if [[ -e "$TARGET" && $FORCE -ne 1 ]]; then
  printf '\033[33m!\033[0m  %s exists. Overwrite? [y/N] ' "$TARGET"
  read -r ans </dev/tty || ans=""
  case "$ans" in
    y|Y|yes|YES) ;;
    *) die "aborted" ;;
  esac
fi

# ── Download ──────────────────────────────────────────────────────
TMP="$(mktemp -t kvmctl.XXXXXX)"
trap 'rm -f "$TMP"' EXIT
info "downloading ${URL}"
if ! curl -fsSL --connect-timeout 10 "$URL" -o "$TMP"; then
  die "download failed — is the portal reachable?"
fi
if [[ ! -s "$TMP" ]]; then
  die "downloaded file is empty"
fi

# ── Install ───────────────────────────────────────────────────────
$SUDO install -m 0755 "$TMP" "$TARGET"
ok "installed ${TARGET}"

# Strip macOS quarantine so Gatekeeper doesn't block the first run.
if [[ "$os" == "darwin" ]] && command -v xattr >/dev/null 2>&1; then
  $SUDO xattr -dr com.apple.quarantine "$TARGET" 2>/dev/null || true
fi

# ── Verify + next-step hint ───────────────────────────────────────
if ! command -v "$BIN_NAME" >/dev/null 2>&1 && [[ ":$PATH:" != *":${INSTALL_DIR}:"* ]]; then
  printf '\033[33m!\033[0m  %s is not on your PATH. Add with:\n    export PATH="%s:$PATH"\n' "$INSTALL_DIR" "$INSTALL_DIR"
fi

"$TARGET" version || die "binary failed to execute"
cat <<EOF

Next: mint an API token at ${PORTAL%:*}:8190/settings/tokens then log in:

  ${BIN_NAME} login --server https://<kvm-center-host>:8190 --token kvmc_…
EOF
