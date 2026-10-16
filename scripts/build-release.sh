#!/usr/bin/env bash
# Compile the public release matrix from a freshly embedded production SPA.
# Never use this as evidence that an artifact was exercised on its target OS.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
OUT="${1:-dist}"
VERSION="${VERSION:-$(tr -d '[:space:]' < server/VERSION)}"
GIT_SHA="${GIT_SHA:-$(git rev-parse HEAD)}"
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][A-Za-z0-9.-]+)?$ ]] || {
  echo "Invalid release version: $VERSION" >&2; exit 1;
}
[[ "$VERSION" == "$(tr -d '[:space:]' < server/VERSION)" ]] || {
  echo "Release version must match server/VERSION" >&2; exit 1;
}
[[ -s server/staticv4/dist/index.html ]] || {
  echo "Production SPA missing. Run scripts/build-webui.sh first." >&2; exit 1;
}
mkdir -p "$OUT"
build() {
  local os="$1" arch="$2" ext="$3" pkg="$4" name="$5" variable="$6"
  local out="$OUT/${name}-${os}-${arch}${ext}"
  echo "Building $out"
  # Explicit GitSHA above works in isolated worktrees/build contexts too.
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -mod=readonly -buildvcs=false -trimpath \
    -ldflags="-s -w -X main.${variable}=${VERSION} -X main.GitSHA=${GIT_SHA}" \
    -o "$out" "./${pkg}"
}
for arch in amd64 arm64; do
  # Version in server and kvm-tools is initialized from versionRaw, so
  # stamping Version itself would be overwritten by package initialization.
  build linux "$arch" "" server kvm-center-server versionRaw
  build linux "$arch" "" agent kvm-center-agent Version
  build linux "$arch" "" cmd/kvmctl kvmctl cliVersion
  build linux "$arch" "" kvm-tools kvm-tools versionRaw
done
build windows amd64 .exe kvm-tools kvm-tools versionRaw
build windows amd64 .exe cmd/kvmctl kvmctl cliVersion
build darwin amd64 "" cmd/kvmctl kvmctl cliVersion
build darwin arm64 "" cmd/kvmctl kvmctl cliVersion
# Enumerate only this matrix; don't accidentally include stale local files.
(cd "$OUT" && sha256sum \
  kvm-center-server-linux-{amd64,arm64} kvm-center-agent-linux-{amd64,arm64} \
  kvm-tools-linux-{amd64,arm64} kvm-tools-windows-amd64.exe \
  kvmctl-linux-{amd64,arm64} kvmctl-windows-amd64.exe \
  kvmctl-darwin-{amd64,arm64} > SHA256SUMS)
