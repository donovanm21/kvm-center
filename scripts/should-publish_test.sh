#!/usr/bin/env bash
# Unit tests for should-publish.sh. Mirrors the cases in the v5.49
# implementation plan PR-1a acceptance criteria.

set -euo pipefail

cd "$(dirname "$0")"
SCRIPT="./should-publish.sh"

pass=0
fail=0

assert() {
  local desc="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    printf '  ok   %s\n' "$desc"
    pass=$((pass + 1))
  else
    printf '  FAIL %s — expected %q, got %q\n' "$desc" "$expected" "$actual"
    fail=$((fail + 1))
  fi
}

echo "should-publish.sh tests"

# Patch bump without [publish] → false.
got=$("$SCRIPT" v5.48.7 v5.48.8 "fix(webui): some patch")
assert "patch bump, no [publish]" "false" "$got"

# Patch bump WITH [publish] → true.
got=$("$SCRIPT" v5.48.7 v5.48.8 "fix(webui): security CVE [publish]")
assert "patch bump, with [publish]" "true" "$got"

# Patch bump with both [deploy] and [publish] → true.
got=$("$SCRIPT" v5.48.7 v5.48.8 "fix(...): X [deploy] [publish]")
assert "patch bump, [deploy] + [publish]" "true" "$got"

# Minor bump → true.
got=$("$SCRIPT" v5.48.8 v5.49.0 "release(v5.49.0): docker hub publish")
assert "minor bump" "true" "$got"

# Major bump → true.
got=$("$SCRIPT" v5.48.8 v6.0.0 "release(v6.0.0): clean break")
assert "major bump" "true" "$got"

# No change → false.
got=$("$SCRIPT" v5.48.8 v5.48.8 "docs: typo fix [publish]")
assert "no version change ignores [publish]" "false" "$got"

# Tolerant of missing 'v' prefix.
got=$("$SCRIPT" 5.48.8 5.49.0 "...")
assert "missing v prefix, minor bump" "true" "$got"

# Tolerant of mixed 'v' prefix.
got=$("$SCRIPT" v5.48.8 5.49.0 "...")
assert "mixed v prefix" "true" "$got"

# Empty commit subject (e.g. merge with no body) — patch bump → false.
got=$("$SCRIPT" v5.48.7 v5.48.8 "")
assert "empty subject, patch" "false" "$got"

echo
echo "result: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
