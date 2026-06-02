#!/usr/bin/env bash
# demo-reset.sh — wipe the demo Mongo + re-seed + restart agents.
# Designed to be called from cron once a day (see demo-up.sh for
# the crontab line) so the demo is always fresh-looking.
#
# Faster than demo-up.sh — doesn't rebuild images.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEMO_COMPOSE="$REPO_ROOT/demo/docker-compose.demo.yml"

PROJECT_NAME="$(basename "$(dirname "$DEMO_COMPOSE")" | sed 's|^demo$||' | tr -d '-')"
# `docker compose -f path/to/demo/docker-compose.demo.yml` uses the
# `name:` directive in the compose file (kvm-center-demo). Resolve the
# volume names once so we can drop the CA + TLS volumes between resets
# — anything we don't drop persists across the wipe and gives a
# visitor a permanent foothold (the cluster CA private key in
# particular). Dropping the data volume happens implicitly via the
# seed re-running against a wiped Mongo; only the side-volumes need
# explicit removal.
DEMO_PROJECT="kvm-center-demo"

echo "$(date -Iseconds)  demo-reset: full down + side-volume wipe"
# `down` here also stops the server, so the seed runs against a stopped
# server and we restart it explicitly below. -v drops the named volumes
# *belonging to this compose project*; --remove-orphans tidies up.
docker compose -f "$DEMO_COMPOSE" down -v --remove-orphans 2>&1 | grep -v "^$" || true

# Belt-and-braces: explicitly remove the TLS + CA volumes in case a
# previous reset of an older compose file left them behind under a
# different project name. CA private key lives here — must not survive.
for vol in "${DEMO_PROJECT}_kvm-center-demo-tls" "${DEMO_PROJECT}_kvm-center-demo-ca"; do
  if docker volume inspect "$vol" >/dev/null 2>&1; then
    docker volume rm "$vol" 2>&1 | grep -v "^$" || true
  fi
done

echo "$(date -Iseconds)  demo-reset: bringing stack back up + seed"
docker compose -f "$DEMO_COMPOSE" up -d kvm-center-demo-mongo kvm-center-demo-server
docker compose -f "$DEMO_COMPOSE" run --rm kvm-center-demo-seed
docker compose -f "$DEMO_COMPOSE" restart kvm-center-demo-server >/dev/null
docker compose -f "$DEMO_COMPOSE" up -d kvm-center-demo-agents

echo "$(date -Iseconds)  demo-reset: done"
