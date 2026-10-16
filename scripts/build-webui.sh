#!/usr/bin/env bash
# A Go-only build embeds just .gitkeep. Release builds must embed the real SPA.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/webui-react"
npm ci --no-audit --no-fund --loglevel=error
npm run build
test -s dist/index.html
# Do not replace a known-good embedded tree until TypeScript + Vite succeed.
rm -rf "$ROOT/server/staticv4/dist"
cp -R dist "$ROOT/server/staticv4/dist"
