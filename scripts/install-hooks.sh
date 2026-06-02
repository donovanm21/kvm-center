#!/usr/bin/env bash
# install-hooks.sh — symlink scripts/pre-commit.sh into .git/hooks/pre-commit.
#
# Idempotent: re-running re-points the symlink without complaint. If
# .git/hooks/pre-commit already exists as a regular file (e.g. an old
# hand-written hook), aborts so the operator can rescue it.
#
# Usage: ./scripts/install-hooks.sh   (or: make hooks)

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
hooks_dir="$repo_root/.git/hooks"
target="$hooks_dir/pre-commit"
source_rel="../../scripts/pre-commit.sh"

if [ ! -d "$hooks_dir" ]; then
	echo "install-hooks: $hooks_dir doesn't exist — is this a git repository?"
	exit 1
fi

if [ -e "$target" ] && [ ! -L "$target" ]; then
	echo "install-hooks: $target already exists as a regular file."
	echo "Move or delete it before re-running this installer."
	exit 1
fi

ln -sf "$source_rel" "$target"
chmod +x "$repo_root/scripts/pre-commit.sh"
echo "✓ Installed pre-commit hook → scripts/pre-commit.sh"
echo "  Bypass any time with: SKIP_PRE_COMMIT=1 git commit ..."
