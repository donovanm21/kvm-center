#!/usr/bin/env bash
# Run the Playwright Tier 2 E2E suite against a demo deployment.
# Containerised so the host doesn't need node / Playwright browsers.
#
# Usage:
#   scripts/run-e2e.sh                    # all specs, default demo URL
#   KVMC_DEMO_URL=https://10.10.100.8:8290 scripts/run-e2e.sh
#   scripts/run-e2e.sh specs/auth.spec.ts # one spec
#
# Output: standard Playwright list reporter; failures land in
# demo/e2e/output/ with traces + screenshots.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
E2E_DIR="$ROOT/demo/e2e"
IMG="kvmc-e2e"

export KVMC_DEMO_URL="${KVMC_DEMO_URL:-https://localhost:8290}"

if ! docker image inspect "$IMG" >/dev/null 2>&1; then
  echo "==> Building $IMG"
  docker build -t "$IMG" "$E2E_DIR"
fi

DOCKER_URL="${KVMC_DEMO_URL//localhost/host.docker.internal}"

# Bind-mount specs + config so edits don't need a rebuild.
docker run --rm \
  --add-host=host.docker.internal:host-gateway \
  -e KVMC_DEMO_URL="$DOCKER_URL" \
  -v "$E2E_DIR/specs:/work/specs" \
  -v "$E2E_DIR/playwright.config.ts:/work/playwright.config.ts" \
  -v "$E2E_DIR/output:/work/output" \
  "$IMG" "$@"
