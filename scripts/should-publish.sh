#!/usr/bin/env bash
# v5.49 publish-gate logic, factored out of the workflow file so it
# can be unit-tested locally.
#
# Outputs `true` (publish) or `false` (skip) on stdout. Exit code
# always 0 on a clean run; non-zero only on bad invocation.
#
# Logic (per design spec §"Trigger logic detail"):
#   - VERSION unchanged → skip.
#   - Minor / major bump → publish.
#   - Patch bump → publish only if commit subject contains [publish].
#   - workflow_dispatch path bypasses this entirely (caller checks).
#
# Usage:
#   should-publish.sh <prev-version> <cur-version> <commit-subject>
#
# Examples (covered by should-publish_test.sh):
#   v5.48.7 v5.48.8 "fix(...)"               → false  (patch, no [publish])
#   v5.48.7 v5.48.8 "fix(...) [publish]"     → true   (patch, override)
#   v5.48.8 v5.49.0 "release(...)"           → true   (minor bump)
#   v5.48.8 v6.0.0  "release(...)"           → true   (major bump)
#   v5.48.8 v5.48.8 "..."                    → false  (no change)

set -euo pipefail

if [ $# -ne 3 ]; then
  echo "usage: $0 <prev-version> <cur-version> <commit-subject>" >&2
  exit 2
fi

prev="$1"
cur="$2"
subject="$3"

# No version change → never publish.
if [ "$prev" = "$cur" ]; then
  echo "false"
  exit 0
fi

# Parse minor segments, tolerant of leading 'v' and missing prefix.
# Example: v5.48.7 → "5.48"; 5.48.7 → "5.48"; v6.0.0 → "6.0".
prev_minor=$(printf '%s' "$prev" | sed 's/^v//' | cut -d. -f1-2)
cur_minor=$(printf '%s' "$cur"  | sed 's/^v//' | cut -d. -f1-2)

# Same major.minor → patch bump → publish only if subject says so.
if [ "$prev_minor" = "$cur_minor" ]; then
  if printf '%s' "$subject" | grep -q '\[publish\]'; then
    echo "true"
  else
    echo "false"
  fi
  exit 0
fi

# Different major or minor → publish.
echo "true"
