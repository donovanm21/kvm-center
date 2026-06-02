#!/usr/bin/env bash
# pre-commit.sh — fast local mirrors of the cheap CI checks.
#
# Catches: gofmt drift, `go vet` errors, TypeScript type errors. These
# are the failures that cost 5+ minutes of CI cycle time without the
# benefit of running tests we couldn't run locally — they're pure
# correctness signals that fail at compile time.
#
# Skips checks when no relevant files are staged. Bypass with
# `SKIP_PRE_COMMIT=1 git commit ...` for emergency commits.
#
# Wired in via scripts/install-hooks.sh (or `make hooks`).

set -o pipefail

if [ "${SKIP_PRE_COMMIT:-}" = "1" ]; then
	echo "pre-commit: SKIP_PRE_COMMIT=1 — skipping checks"
	exit 0
fi

# Staged files (added/copied/modified/renamed). Sourced from the index,
# not the working tree, so `git add -p` partial commits are honoured.
# `mapfile` is bash-4-only (not on stock macOS bash 3.x), so we use a
# portable while-read newline-separated read instead.
GO_FILES=""
TS_FILES=""
while IFS= read -r f; do
	[ -z "$f" ] && continue
	case "$f" in
		*.go)        GO_FILES="$GO_FILES$f"$'\n' ;;
		*.ts|*.tsx)  TS_FILES="$TS_FILES$f"$'\n' ;;
	esac
done < <(git diff --cached --name-only --diff-filter=ACMR)

# Strip trailing newline so `printf %s` and `xargs -0` behave.
GO_FILES="${GO_FILES%$'\n'}"
TS_FILES="${TS_FILES%$'\n'}"

if [ -z "$GO_FILES" ] && [ -z "$TS_FILES" ]; then
	exit 0
fi

fail=0

# 1. gofmt — list-only. Auto-rewriting would silently mutate the
#    committed snapshot from under the operator; clearer to fail
#    with an actionable command.
if [ -n "$GO_FILES" ]; then
	if ! command -v gofmt >/dev/null 2>&1; then
		# v5.60.17 — soft-skip when the Go toolchain isn't installed.
		# Previously we hard-failed and forced SKIP_PRE_COMMIT=1,
		# which also bypassed the tsc check. Now we just warn and
		# let the remaining checks run; CI catches gofmt drift.
		echo "pre-commit: gofmt not on PATH — skipping Go checks (CI will catch them)"
		GO_FILES=""
	fi
fi
if [ -n "$GO_FILES" ]; then
	# shellcheck disable=SC2086  # we want word-splitting here
	needs_fmt=$(printf '%s\n' "$GO_FILES" | xargs gofmt -l 2>/dev/null || true)
	if [ -n "$needs_fmt" ]; then
		echo "✗ gofmt: these staged files need formatting:"
		echo "$needs_fmt" | sed 's/^/    /'
		echo ""
		echo "  fix:   gofmt -w $(echo "$needs_fmt" | tr '\n' ' ')"
		echo "  retry: git add -u && git commit ..."
		fail=1
	fi
fi

# 2. go vet — covers the whole tree even when only one file changed.
#    `go vet` against ./... is fast once the build cache is warm
#    (~2 s typical) and catches the type-mismatch class that bit us
#    on PR #86 (audit() taking map[string]string vs map[string]any).
if [ -n "$GO_FILES" ] && [ "$fail" -eq 0 ]; then
	if ! command -v go >/dev/null 2>&1; then
		# v5.60.17 — soft-skip; CI still catches everything.
		echo "pre-commit: go not on PATH — skipping go vet (CI will catch it)"
		GO_FILES=""
	fi
fi
if [ -n "$GO_FILES" ] && [ "$fail" -eq 0 ]; then
	if ! go vet ./...; then
		echo ""
		echo "✗ go vet failed (output above)"
		fail=1
	fi
fi

# 3. tsc --noEmit — only when .ts / .tsx changed. Slower than vet
#    (~10-30 s on cold cache, ~5 s on warm), still cheaper than a
#    push + CI round-trip. Uses npx --no-install so it doesn't
#    silently fetch a typescript build tool we don't already have.
if [ -n "$TS_FILES" ] && [ "$fail" -eq 0 ]; then
	if [ ! -d webui-react/node_modules ]; then
		# v5.60.17 — soft-skip; CI still catches everything.
		echo "pre-commit: webui-react/node_modules missing — skipping tsc (CI will catch it). Run \`npm --prefix webui-react ci\` to enable local checks."
		TS_FILES=""
	fi
fi
if [ -n "$TS_FILES" ] && [ "$fail" -eq 0 ]; then
	if ! (cd webui-react && npx --no-install tsc --noEmit); then
		echo ""
		echo "✗ tsc --noEmit failed (output above)"
		fail=1
	fi
fi

if [ "$fail" -ne 0 ]; then
	echo ""
	echo "Bypass with SKIP_PRE_COMMIT=1 git commit ... if you really need to."
	exit 1
fi

echo "✓ pre-commit checks passed"
