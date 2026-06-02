#!/usr/bin/env bash
# wait-ci.sh — block until Gitea CI lands a terminal state on a ref.
#
# Designed to be run via `Bash run_in_background: …` so we get a single
# completion notification when CI flips instead of polling-with-sleeps
# from the foreground. Prints one line per status transition while
# in-flight, then a final summary; exits non-zero on failure so the
# notification banner is unambiguous.
#
# Usage:
#   GITEA_USER=jeff GITEA_PASS=Access2Git \
#     scripts/wait-ci.sh release/v5.60.7
#
#   # or with an explicit commit SHA:
#   GITEA_USER=… GITEA_PASS=… scripts/wait-ci.sh 1e11040
#
# Env knobs:
#   GITEA_HOST   REQUIRED. e.g. host.example:9001 — no default, on purpose,
#                to keep this repo free of operator-specific identifiers.
#   GITEA_REPO   default Homelab/kvm-center
#   POLL_SECS    default 20  (Gitea repaints checks roughly every 10–20 s)
#   MAX_SECS     default 1800 (30 min hard cap; abort with a clear error)
#
# Exit codes:
#   0  combined state success
#   1  combined state failure / error / timeout
#   2  usage error
#
# This is intentionally read-only against Gitea — no merge, no tag, no
# push. Wrapping a merge in here would conflate "tell me when CI flips"
# with "commit the next action," which is the operator's call.

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <branch|sha>" >&2
  exit 2
fi
ref="$1"

: "${GITEA_REPO:=Homelab/kvm-center}"
: "${POLL_SECS:=20}"
: "${MAX_SECS:=1800}"

if [[ -z "${GITEA_USER:-}" || -z "${GITEA_PASS:-}" ]]; then
  echo "wait-ci: GITEA_USER and GITEA_PASS must be set" >&2
  exit 2
fi
if [[ -z "${GITEA_HOST:-}" ]]; then
  echo "wait-ci: GITEA_HOST must be set (e.g. host.example:9001) — no default to keep this repo identifier-free" >&2
  exit 2
fi

url="http://${GITEA_HOST}/api/v1/repos/${GITEA_REPO}/commits/${ref}/status"
started=$(date +%s)
prev_state=""
consec_fail=0
# Per-check previous state, encoded as "ctx=state ctx=state …" so we can
# diff each tick with a single grep. Avoids associative arrays (the e2e
# battery's bash-3.2 lesson).
prev_checks=""

# fetch_status: print the combined state on the first stdout line, then
# one "<context>=<state>" per check. Tolerates transient curl errors so
# a 30-second network blip doesn't kill the watch.
fetch_status() {
  local body
  body=$(curl -s --connect-timeout 5 --max-time 15 -u "${GITEA_USER}:${GITEA_PASS}" "$url" 2>/dev/null || true)
  if [[ -z "$body" ]]; then
    return 1
  fi
  python3 - "$body" <<'PY' || return 1
import json, sys
try:
    d = json.loads(sys.argv[1])
except Exception:
    sys.exit(1)
print(d.get("state") or "unknown")
for s in d.get("statuses", []):
    ctx = (s.get("context") or "").strip().replace(" ", "_")
    state = s.get("state") or "pending"
    print(f"{ctx}={state}")
PY
}

while :; do
  now=$(date +%s)
  if (( now - started > MAX_SECS )); then
    echo "CI: timeout after ${MAX_SECS}s waiting on $ref"
    exit 1
  fi

  if ! out=$(fetch_status); then
    consec_fail=$((consec_fail + 1))
    # Be loud after a few failures so silent infinite-loops are impossible.
    if (( consec_fail == 3 )); then
      echo "CI: cannot reach $url (3 consecutive empty/error responses) — check GITEA_HOST + creds"
    fi
    if (( consec_fail >= 10 )); then
      echo "CI: giving up after $consec_fail consecutive fetch failures against $url"
      exit 1
    fi
    sleep "$POLL_SECS"
    continue
  fi
  consec_fail=0

  combined=$(printf '%s\n' "$out" | head -1)
  checks=$(printf '%s\n' "$out" | tail -n +2 | sort)

  # Per-check transitions: emit a line for each check whose state
  # changed since the last tick. comm -13 prints lines only in $checks.
  if [[ "$checks" != "$prev_checks" ]]; then
    while IFS= read -r line; do
      [[ -z "$line" ]] && continue
      ctx="${line%%=*}"
      state="${line#*=}"
      # Normalise nullish "None" from the API to "queued" so the
      # transition log reads cleanly.
      [[ "$state" == "None" ]] && state="queued"
      echo "CI: ${ctx} → ${state}"
    done < <(comm -13 <(printf '%s\n' "$prev_checks") <(printf '%s\n' "$checks"))
    prev_checks="$checks"
  fi

  if [[ "$combined" != "$prev_state" ]]; then
    echo "CI: combined state = $combined"
    prev_state="$combined"
  fi

  case "$combined" in
    success)
      echo "CI: ✓ all checks passed on $ref"
      exit 0
      ;;
    failure|error)
      echo "CI: ✗ $combined on $ref"
      # Dump final check states for the notification banner.
      printf '%s\n' "$checks" | sed 's/^/CI:   /'
      exit 1
      ;;
    pending|"")
      sleep "$POLL_SECS"
      ;;
    *)
      echo "CI: unexpected combined state '$combined' — treating as failure"
      exit 1
      ;;
  esac
done
