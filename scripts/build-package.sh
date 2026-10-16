#!/usr/bin/env bash
# Non-publishing entry point: always rebuild the production SPA before stamping
# the native release matrix. Workflow adoption requires a separate change.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bash "$ROOT/scripts/build-webui.sh"
exec bash "$ROOT/scripts/build-release.sh" "${1:-$ROOT/dist}"
