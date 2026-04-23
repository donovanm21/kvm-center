#!/usr/bin/env bash
# Record walkthrough clips against the demo stack.
#
# If node+ffmpeg are on PATH, runs locally.
# Otherwise falls back to the bundled docker image (Playwright + ffmpeg).
#
# Usage:
#   KVMC_DEMO_URL=https://REDACTED-HOST:8290 scripts/record-walkthroughs.sh
#   scripts/record-walkthroughs.sh 01-login-dashboard   # single flow
#
# Output: server/docs/media/<flow>.webp

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WT_DIR="$ROOT/demo/walkthroughs"
OUT_DIR="$ROOT/server/docs/media"
mkdir -p "$OUT_DIR"

export KVMC_DEMO_URL="${KVMC_DEMO_URL:-https://localhost:8290}"
FLOW_FILTER="${1:-}"

if command -v node >/dev/null && command -v npm >/dev/null && command -v ffmpeg >/dev/null; then
  echo "==> Using local node + ffmpeg"
  (cd "$WT_DIR" && npm install --silent)
  OUT_DIR="$OUT_DIR" exec "$WT_DIR/run.sh" $FLOW_FILTER
fi

echo "==> node/ffmpeg not found; using docker image"
IMG="kvmc-walkthroughs"
if ! docker image inspect "$IMG" >/dev/null 2>&1; then
  echo "==> Building $IMG"
  docker build -t "$IMG" "$WT_DIR"
fi

# host.docker.internal works on mac/win Docker Desktop; on linux the
# user should either pass KVMC_DEMO_URL=http(s)://<host-ip>:8290
# explicitly or use --network host.
DOCKER_URL="${KVMC_DEMO_URL//localhost/host.docker.internal}"

docker run --rm \
  --add-host=host.docker.internal:host-gateway \
  -e KVMC_DEMO_URL="$DOCKER_URL" \
  -v "$OUT_DIR:/out" \
  -v "$WT_DIR/specs:/work/specs" \
  -v "$WT_DIR/playwright.config.ts:/work/playwright.config.ts" \
  -v "$WT_DIR/run.sh:/work/run.sh" \
  "$IMG" $FLOW_FILTER

echo "==> Done. Clips in $OUT_DIR"
