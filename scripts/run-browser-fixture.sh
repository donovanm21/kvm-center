#!/usr/bin/env bash
# Run real browser + API assertions against fresh Mongo (default) or JSON.
# No synthetic command agent and no real hypervisor. Requires local Mongo unless JSON,
# npm ci --prefix demo/e2e, and installed Playwright chromium + webkit browsers.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
candidate_status="$(git status --porcelain)"
if [[ -n "$candidate_status" ]]; then
  echo 'Browser fixture requires a clean committed candidate; Git SHA cannot identify uncommitted source changes.' >&2
  exit 1
fi
candidate_sha="$(git rev-parse HEAD)"
verify_candidate() {
  local current_sha current_status
  current_sha="$(git rev-parse HEAD)"
  current_status="$(git status --porcelain)"
  if [[ "$current_sha" != "$candidate_sha" || -n "$current_status" ]]; then
    echo 'Browser fixture source changed; rerun against a frozen clean candidate.' >&2
    exit 1
  fi
}
export KVMC_TEST_BACKEND="${KVMC_TEST_BACKEND:-mongo}"
case "$KVMC_TEST_BACKEND" in
  mongo)
    : "${KVMC_TEST_MONGO_URI:?set KVMC_TEST_MONGO_URI to a disposable loopback MongoDB}"
    KVMC_TEST_MONGO_URI="$(python3 - "$KVMC_TEST_MONGO_URI" <<'PYMONGO'
import sys
from urllib.parse import urlparse
from scripts.validate import local_test_url
if not local_test_url(sys.argv[1], ('mongodb',)):
    raise SystemExit('Only a single unauthenticated loopback MongoDB without driver options is accepted')
print(urlparse(sys.argv[1])._replace(query='directConnection=true').geturl())
PYMONGO
)"
    ;;
  json) KVMC_TEST_MONGO_URI="" ;;
  *) echo 'KVMC_TEST_BACKEND must be mongo or json' >&2; exit 1 ;;
esac
python3 - "${KVMC_TEST_PORT:-18290}" <<'PYPORT'
import socket, sys
port = int(sys.argv[1])
if not 1024 <= port <= 65535:
    raise SystemExit('Fixture port must be between 1024 and 65535')
with socket.socket() as sock:
    sock.bind(('127.0.0.1', port))
PYPORT
RUN="$(mktemp -d "${TMPDIR:-/tmp}/kvmc-browser-XXXXXXXX")"
export KVM_CENTER_DEMO_MONGO_URL="$KVMC_TEST_MONGO_URI"
export KVM_CENTER_DEMO_MONGO_DB="kvmc_validation_browser_$(python3 -c 'import secrets; print(secrets.token_hex(12))')"
export KVMC_FIXTURE_DATABASE="$KVM_CENTER_DEMO_MONGO_DB"
export KVMC_VALIDATION_FIXTURE=1
export KVMC_DISPOSABLE_DEMO=1
export KVMC_DEMO_URL="https://127.0.0.1:${KVMC_TEST_PORT:-18290}"
SERVER_PID=""
SEEDED=0
cleanup() {
  local rc=$?
  trap - EXIT
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [[ "$SEEDED" == 1 ]]; then
    if ! KVMC_VALIDATION_CLEANUP=1 "$RUN/seed" >> "$RUN/seed.log" 2>&1; then
      echo "Fixture cleanup failed: $KVM_CENTER_DEMO_MONGO_DB; inspect $RUN/seed.log" >&2
      rc=1
    fi
  fi
  # Keep only logs for diagnosis; remove generated CA/private keys and binaries.
  rm -rf "$RUN/ca" "$RUN/tls" "$RUN/server" "$RUN/seed" "$RUN/data.json"
  echo "Fixture logs: $RUN"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
./scripts/build-webui.sh
go build -mod=readonly -buildvcs=false -o "$RUN/seed" ./demo/seed
go build -mod=readonly -buildvcs=false -ldflags="-X main.GitSHA=$candidate_sha" -o "$RUN/server" ./server
verify_candidate
if [[ "$KVMC_TEST_BACKEND" == mongo ]]; then
  # Mark before seed begins so a partial seed is also cleaned.
  SEEDED=1
  "$RUN/seed" > "$RUN/seed.log" 2>&1
else
  KVMC_VALIDATION_JSON="$RUN/data.json" "$RUN/seed" > "$RUN/seed.log" 2>&1
fi
printf 'Browser fixture backend: %s; inventory: synthetic; real hypervisors: none\n' "$KVMC_TEST_BACKEND"
(
  # Do not inherit production listeners, certificate paths or demo bypasses.
  for setting in ${!KVM_CENTER_@}; do unset "$setting"; done
  unset KVMC_AUTO_APPROVE_CIDR
  export KVM_CENTER_MONGO_URL="$KVMC_TEST_MONGO_URI"
  export KVM_CENTER_MONGO_DB="$KVMC_FIXTURE_DATABASE"
  export KVM_CENTER_DATA="$RUN/data.json" KVM_CENTER_CA_DIR="$RUN/ca" KVM_CENTER_TLS_DIR="$RUN/tls"
  export KVM_CENTER_LEGACY_HTTP=0
  exec "$RUN/server" -no-tls=false -addr 127.0.0.1:0 \
    -tls-addr "127.0.0.1:${KVMC_TEST_PORT:-18290}" -grpc-addr "" -cert "" -key ""
) > "$RUN/server.log" 2>&1 &
SERVER_PID=$!
ready=0
for _ in $(seq 1 60); do
  kill -0 "$SERVER_PID" 2>/dev/null || { cat "$RUN/server.log"; exit 1; }
  if curl --fail --silent --insecure --connect-timeout 1 --max-time 2 "$KVMC_DEMO_URL/health" >/dev/null; then ready=1; break; fi
  sleep 1
done
[[ "$ready" == 1 ]] || { echo 'Fixture failed to become healthy' >&2; exit 1; }
python3 scripts/validate.py --profile browser
verify_candidate
