#!/usr/bin/env bash
# demo-reset.sh — wipe the demo Mongo + re-seed + restart agents.
# Designed to be called from cron once a day (see demo-up.sh for
# the crontab line) so the demo is always fresh-looking.
#
# Faster than demo-up.sh — doesn't rebuild images.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEMO_COMPOSE="$REPO_ROOT/demo/docker-compose.demo.yml"

echo "$(date -Iseconds)  demo-reset: stopping agents container"
docker compose -f "$DEMO_COMPOSE" stop kvm-center-demo-agents

echo "$(date -Iseconds)  demo-reset: running seed (drops + reinserts)"
docker compose -f "$DEMO_COMPOSE" run --rm kvm-center-demo-seed

echo "$(date -Iseconds)  demo-reset: restarting server"
docker compose -f "$DEMO_COMPOSE" restart kvm-center-demo-server >/dev/null

echo "$(date -Iseconds)  demo-reset: starting agents container"
docker compose -f "$DEMO_COMPOSE" start kvm-center-demo-agents

echo "$(date -Iseconds)  demo-reset: done"
