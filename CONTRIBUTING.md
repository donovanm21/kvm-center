# Contributing to kvm-center

Thanks for considering a contribution. This project is maintained by a
small team in their spare time; a little structure on incoming work
goes a long way.

## Before you start

- **Open an issue first for anything beyond a small fix.** Describe
  the problem, the proposed approach, and why it matters. This saves
  both of us the frustration of a PR that gets declined because the
  scope doesn't fit the roadmap.
- **Security issues go private.** See [SECURITY.md](SECURITY.md) — do
  not open a public issue for a vulnerability.
- **Check the [Code of Conduct](CODE_OF_CONDUCT.md).**

## The fork → PR flow

kvm-center uses the standard GitHub fork-and-pull-request model:

1. **Fork** the repo on GitHub (`donovanm21/kvm-center` → your account).
2. **Clone** your fork and add the canonical repo as `upstream`:
   ```sh
   git clone https://github.com/<you>/kvm-center
   cd kvm-center
   git remote add upstream https://github.com/donovanm21/kvm-center
   ```
3. **Branch** off `main` (`feature/<short-name>` or `fix/<short-name>`):
   ```sh
   git checkout -b fix/migration-pool-picker main
   ```
4. **Make your change**, with tests where it makes sense (see below).
5. **Push** to your fork:
   ```sh
   git push -u origin fix/migration-pool-picker
   ```
6. **Open a pull request** against `donovanm21/kvm-center:main`. CI
   runs automatically on the PR; a maintainer reviews once it's green.

Keep your branch current with `git fetch upstream && git rebase
upstream/main` if review takes a while.

## Development setup

Requirements:

- Docker + Docker Compose plugin (v2)
- Go 1.26.9+ (optional if you build inside Docker)
- Node ^20.19.0 or >=22.12.0 (for the React SPA)
- `make`, `git`, `curl`

### Run the server + Mongo locally

The bootstrap script can build and start the full Docker stack:

```sh
./scripts/bootstrap.sh --stack
```

This runs `docker compose build` + `docker compose up -d` and brings
the UI up on `https://localhost:8190` (TLS-only since v5.32.5 — accept
the self-signed cert on first load). The same stack is also available
through `make`:

```sh
make docker-up      # docker compose up -d
make docker-logs    # docker compose logs -f
make docker-down    # docker compose down
```

For a quick non-Docker server loop against a local JSON file (no Mongo):

```sh
make run-server     # KVM_CENTER_DATA=./data.json go run ./server -addr :8090
```

### Frontend (React SPA)

The web UI lives in `webui-react/` and is built into the server's
embedded filesystem for release. For a hot-reload loop:

```sh
cd webui-react
npm install
npm run dev          # Vite dev server; proxy is configured for :8190 (TLS)
```

## Tests, linting, and build gates

Start with `make validate`. It records source, race-test, frontend and release
cross-build evidence. Real MongoDB and browser fixture checks are separate;
see [validation and the feature matrix](docs/validation.md). Missing services
or release sources are blockers, not successful tests.

The smaller commands below remain useful while iterating:

```sh
# Go tests across every package
make test            # go test ./...

# Lint gate: gofmt check (no writes) + go vet, plus golangci-lint if
# it's on PATH (skipped otherwise)
make lint

# Format Go in place
make fmt             # gofmt -w .

# Small Go-only gate: lint + tests (not release validation)
make check

# Frontend type-check + production build
cd webui-react && npm run build   # tsc -b --noEmit && vite build
```

You can also call the underlying tools directly: `go test ./...`,
`gofmt -l .` (lists files that need formatting), `go vet ./...`.

### (recommended) Install the pre-commit hook

```sh
make hooks
```

It runs `gofmt`, `go vet`, and `tsc --noEmit` on staged files, so the
cheap CI failures surface locally instead of after a round-trip. The
hook only runs the check matching the file types you staged —
pure-docs commits skip both Go and TypeScript checks. Bypass in an
emergency with `SKIP_PRE_COMMIT=1 git commit ...`.

## Coding standards

- **Format.** All Go must pass `gofmt` and `go vet`. CI rejects any
  unformatted file.
- **Commit messages.** Conventional-commit style is used throughout the
  history — `type(scope): subject` in the imperative mood, e.g.
  `fix(migrations): pass copy_storage on offline drain` or
  `docs(changelog): collapse to minor versions`. Reference the issue
  number when one exists. The body can be as detailed as it needs to be.
- **Document user-visible changes** in
  [`server/docs/changelog.md`](server/docs/changelog.md). The changelog
  follows the [Keep a Changelog](https://keepachangelog.com) convention
  with **no dates** — version headings only, newest first. Tooling, CI,
  and internal refactors do not go in the changelog.
- **Tests.** New behaviour needs a test; fixed bugs should get a
  regression test where practical.
- **No generated or local files.** Don't commit bundled JS output,
  unnecessary `go.sum` churn, or local `.env` files. `.gitignore`
  should already cover the common cases.

## Pull request expectations

- **CI must pass.** Every PR runs the `test` workflow
  (`.github/workflows/test.yml`), which has these jobs:
  - **`lint-test`** — `go mod tidy`, `gofmt`, `go vet`, `go test ./...`,
    and `govulncheck` (blocking on any reachable vulnerability).
  - **`webui-audit`** — `npm ci`, `npm run build` (tsc + vite), and
    `npm audit --audit-level=critical` (blocking).
  - **`gitleaks`** — full-history secret scan.
  - **`summary`** — aggregator that fails if any of the above fail.
- **Manual acceptance is separate.** The new source/race/Mongo/browser/packaging
  harness is not automatically enabled in CI. Consult the validation matrix;
  missing or blocked runtime evidence must remain visible.
- **Keep PRs focused.** One concern per PR — a bug fix and an unrelated
  refactor belong in separate PRs.
- **Lint clean.** `make lint` must pass locally before you push.

## What we're likely to merge

- Bug fixes, with a clear reproduction.
- Small, well-scoped improvements to existing features.
- Documentation fixes.
- Test coverage for under-tested paths.

## What we're slower to accept

- Large new features without a prior design discussion.
- Refactors that don't come with a concrete motivating bug or
  performance win.
- Changes that depend on one operator's specific environment.

## What we won't merge

- Code without a license-compatible license (the project is Apache-2.0).
- Changes that remove or weaken the security gates in CI.
- Additions that bundle operator-specific secrets, hostnames, or
  network details into the shipped tree.

## Questions

Open a discussion or a low-priority issue, or drop into
[Discord](https://discord.gg/c6yWauUsQh). Response times vary — this is
a side project, not a commercial product.
