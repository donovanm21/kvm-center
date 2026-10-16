#!/usr/bin/env bash
# Read-only, bounded exact-candidate gate. Never publishes or changes CI.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
: "${GITHUB_SHA:?exact candidate SHA is required}"
MAX_WAIT_SECONDS="${MAX_WAIT_SECONDS:-1800}"
POLL_SECONDS="${POLL_SECONDS:-15}"
[[ "$MAX_WAIT_SECONDS" =~ ^[1-9][0-9]*$ && "$POLL_SECONDS" =~ ^[1-9][0-9]*$ ]] || {
  echo 'Wait and poll durations must be positive integers' >&2; exit 1;
}
started=$SECONDS
while :; do
  # gh failure/auth error is not pending and must fail closed immediately.
  runs=$(gh run list --workflow test.yml --event push --commit "$GITHUB_SHA" \
    --json headSha,status,conclusion --limit 1)
  rc=0
  printf '%s\n' "$runs" | python3 scripts/assert-ci-success.py "$GITHUB_SHA" || rc=$?
  if [[ "$rc" == 0 ]]; then exit 0; fi
  if [[ "$rc" != 2 ]]; then exit "$rc"; fi
  if (( SECONDS - started >= MAX_WAIT_SECONDS )); then
    echo "Publication blocked: exact-candidate CI did not pass within ${MAX_WAIT_SECONDS}s" >&2
    exit 1
  fi
  sleep "$POLL_SECONDS"
done
