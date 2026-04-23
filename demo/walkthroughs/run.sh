#!/usr/bin/env bash
# In-container recorder: runs Playwright, converts .webm → .webp,
# drops clips in $OUT_DIR (default /out).

set -euo pipefail

OUT_DIR="${OUT_DIR:-/out}"
VID_DIR="/work/output/videos"

mkdir -p "$OUT_DIR"
rm -rf "$VID_DIR"

echo "==> Recording against ${KVMC_DEMO_URL:-https://localhost:8290}"
if [[ $# -gt 0 ]]; then
  npx playwright test "specs/${1}.spec.ts"
else
  npx playwright test
fi

echo "==> Converting .webm → .webp"
shopt -s nullglob
for dir in "$VID_DIR"/*/; do
  raw="$(basename "$dir")"
  # Playwright dir: <spec-stem>-<test-title>-<project>.
  # We name spec-stem === test-title, so after stripping "-desktop"
  # the remainder is "<stem>-<stem>"; take the first half.
  stripped="${raw%-desktop}"
  # shellcheck disable=SC2295
  half=$(( ${#stripped} / 2 ))
  flow="${stripped:0:half}"
  # If the duplication assumption doesn't hold, fall back to the
  # full stripped name.
  if [[ "${stripped:half}" != "-$flow" ]]; then
    flow="$stripped"
  fi
  webm="$(ls "$dir"/*.webm 2>/dev/null | head -n1 || true)"
  [[ -z "$webm" ]] && { echo "   (no video for $raw)"; continue; }
  out="$OUT_DIR/${flow}.webp"
  echo "   $raw → $out"
  ffmpeg -y -loglevel error -i "$webm" \
    -vf "fps=10,scale=1100:-2:flags=lanczos" \
    -c:v libwebp -lossless 0 -q:v 28 -preset picture -compression_level 6 -loop 0 -an \
    "$out"
done

echo "==> Done."
ls -la "$OUT_DIR" | grep '\.webp$' || true
