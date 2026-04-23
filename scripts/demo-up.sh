#!/usr/bin/env bash
# demo-up.sh — bring up the kvm-center demo stack from scratch.
#
# Tears down any existing demo stack (volumes included), rebuilds
# the production server image if missing, spins up Mongo + server +
# seed + 3 × demo-agent, waits for seed to finish, and prints the
# admin credentials + API token.
#
# DEV-ONLY. The demo/ tree is stripped from the public GitHub mirror.
#
# Usage:
#   ./scripts/demo-up.sh
#
# Env overrides:
#   DEMO_PORT          host port for the SPA (default: 8290)
#
# See demo/README.md for the follow-up steps (opening a browser,
# scheduling the auto-reset, etc.).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEMO_COMPOSE="$REPO_ROOT/demo/docker-compose.demo.yml"
DEMO_PORT="${DEMO_PORT:-8290}"

echo "==> tearing down any existing demo stack"
docker compose -f "$DEMO_COMPOSE" down -v --remove-orphans 2>&1 | grep -v "^$" || true

echo "==> ensuring kvm-center-server:latest image exists"
if ! docker image inspect kvm-center-server:latest >/dev/null 2>&1; then
  echo "    image missing; building from $REPO_ROOT/docker-compose.yml"
  docker compose -f "$REPO_ROOT/docker-compose.yml" build kvm-center-server
fi

echo "==> building demo image"
docker compose -f "$DEMO_COMPOSE" build --pull

echo "==> starting Mongo + server"
docker compose -f "$DEMO_COMPOSE" up -d kvm-center-demo-mongo kvm-center-demo-server

echo "==> waiting for server on :$DEMO_PORT"
for i in $(seq 1 60); do
  if curl -sk "https://localhost:$DEMO_PORT/api/version" 2>/dev/null | grep -q version; then
    echo "    server responding"
    break
  fi
  sleep 2
  if [[ $i -eq 60 ]]; then
    echo "    ERROR: server never responded — check 'docker logs kvm-center-demo-server'"
    exit 1
  fi
done

echo "==> running seed"
docker compose -f "$DEMO_COMPOSE" run --rm kvm-center-demo-seed

echo "==> starting demo agents (single container, goroutine per host)"
docker compose -f "$DEMO_COMPOSE" up -d kvm-center-demo-agents

# Restart the server so it reloads state from Mongo after the seed.
# (ApplyTelemetry from agents will keep it fresh, but the initial
# load() only ran before any fixtures existed.)
echo "==> restarting server to pick up seeded state"
docker compose -f "$DEMO_COMPOSE" restart kvm-center-demo-server >/dev/null

# Wait until the post-restart server answers again.
for i in $(seq 1 30); do
  if curl -sk "https://localhost:$DEMO_PORT/api/version" 2>/dev/null | grep -q version; then
    break
  fi
  sleep 1
done

cat <<EOF

───────────────────────────────────────────────────────
  kvm-center DEMO is up

  URL:     https://localhost:$DEMO_PORT
           (self-signed cert — accept the browser warning)

  Login:   admin / demo-password
           operator / demo-operator
           viewer / demo-viewer

  Token:   kvmc_demo0001.00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff
           export KVMCTL_SERVER="https://localhost:$DEMO_PORT"
           export KVMCTL_TOKEN="kvmc_demo0001.00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

  Reset:   ./scripts/demo-reset.sh
  Logs:    docker compose -f demo/docker-compose.demo.yml logs -f

  Auto-reset cron (run 'crontab -e' on the dev host once):
           0 3 * * * $REPO_ROOT/scripts/demo-reset.sh >/tmp/kvmc-demo-reset.log 2>&1
───────────────────────────────────────────────────────
EOF
