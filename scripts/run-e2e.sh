#!/usr/bin/env bash
# Run the Playwright Tier 2 E2E suite against a demo deployment.
# Containerised so the host doesn't need node / Playwright browsers.
#
# Usage:
#   scripts/run-e2e.sh                    # all specs, default demo URL
#   KVMC_DEMO_URL=https://demo.example.com:8290 scripts/run-e2e.sh
#   scripts/run-e2e.sh specs/auth.spec.ts # one spec
#
# Output: standard Playwright list reporter; failures land in
# demo/e2e/output/ with traces + screenshots.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
E2E_DIR="$ROOT/demo/e2e"
IMG="kvmc-e2e"

export KVMC_DEMO_URL="${KVMC_DEMO_URL:-https://localhost:8290}"
export KVMC_EXPECTED_SHA="${KVMC_EXPECTED_SHA:-$(git -C "$ROOT" rev-parse HEAD)}"
export KVMC_EXPECTED_BACKEND="${KVMC_EXPECTED_BACKEND:-mongo}"
if [[ ! "$KVMC_EXPECTED_SHA" =~ ^[a-f0-9]{40}$ ]]; then
  echo "KVMC_EXPECTED_SHA must be the exact 40-character deployed revision" >&2
  exit 2
fi
if [[ "$KVMC_EXPECTED_BACKEND" != mongo && "$KVMC_EXPECTED_BACKEND" != json ]]; then
  echo "KVMC_EXPECTED_BACKEND must be mongo or json" >&2
  exit 2
fi

DOCKER_URL="${KVMC_DEMO_URL//localhost/host.docker.internal}"

if [[ -n "${CI:-}" ]]; then
  # CI mode: this script runs inside a runner job container, so
  # bind-mounting $E2E_DIR/specs would point at a path that exists
  # in the runner's filesystem but not on the docker daemon's host
  # (the daemon does the mount and can't see what the runner sees).
  # Always rebuild the image so the COPY . . step bakes in current
  # specs, and route output through a named volume that we extract
  # with docker cp once the run finishes.
  echo "==> CI mode: rebuilding $IMG with current sources"
  docker build -t "$IMG" "$E2E_DIR"
  VOL="kvmc-e2e-output-$$"
  docker volume create "$VOL" >/dev/null
  set +e
  docker run --rm \
    --add-host=host.docker.internal:host-gateway \
    -e KVMC_DEMO_URL="$DOCKER_URL" \
    -e KVMC_EXPECTED_SHA="$KVMC_EXPECTED_SHA" \
    -e KVMC_EXPECTED_BACKEND="$KVMC_EXPECTED_BACKEND" \
    -v "$VOL:/work/output" \
    "$IMG" "$@"
  rc=$?
  set -e
  # Surface artefacts to the runner workspace so upload-artifact
  # can find them. We can't bind-mount $E2E_DIR/output here (same
  # daemon-vs-runner path mismatch), so spin up a stopped helper
  # with the volume attached and `docker cp` out of it.
  mkdir -p "$E2E_DIR/output"
  HELPER=$(docker create -v "$VOL:/work/output" alpine true)
  docker cp "$HELPER:/work/output/." "$E2E_DIR/output/" 2>/dev/null || true
  docker rm "$HELPER" >/dev/null || true
  docker volume rm "$VOL" >/dev/null || true
  exit $rc
fi

# Dev mode: bind-mount specs + config so edits don't need a rebuild.
if ! docker image inspect "$IMG" >/dev/null 2>&1; then
  echo "==> Building $IMG"
  docker build -t "$IMG" "$E2E_DIR"
fi
docker run --rm \
  --add-host=host.docker.internal:host-gateway \
  -e KVMC_DEMO_URL="$DOCKER_URL" \
    -e KVMC_EXPECTED_SHA="$KVMC_EXPECTED_SHA" \
    -e KVMC_EXPECTED_BACKEND="$KVMC_EXPECTED_BACKEND" \
  -v "$E2E_DIR/specs:/work/specs" \
  -v "$E2E_DIR/playwright.config.ts:/work/playwright.config.ts" \
  -v "$E2E_DIR/output:/work/output" \
  "$IMG" "$@"
