# Contributing to kvm-center

Thanks for considering a contribution. This project is maintained by a
small team in their spare time; a little structure on incoming work
goes a long way.

## Before you start

- **Open an issue first for anything beyond a small fix.** Describe
  the problem, the proposed approach, and why it matters. This saves
  both of us the frustration of a PR that gets declined because the
  scope doesn't fit the roadmap.
- **Security issues go private.** See [SECURITY.md](SECURITY.md).
- **Check the [Code of Conduct](CODE_OF_CONDUCT.md).**

## Development setup

Requirements:

- Docker + Docker Compose plugin (v2)
- Go 1.25+ (optional if you build inside Docker)
- Node 20+ (for the React SPA)
- `make`, `git`, `curl`

Local dev loop:

```sh
# Run the server + Mongo locally
./scripts/bootstrap.sh --stack

# Run Go tests
make test

# Run the lint gate (gofmt + go vet + optional golangci-lint)
make lint

# Run the full quality gate (lint + tests)
make check
```

The web UI lives in `webui-react/` and is built into the server's
embedded filesystem. `npm run dev` from there gives you a hot-reload
loop against a backend running on `:8090`.

## Pull requests

- **One concern per PR.** A bug fix and an unrelated refactor belong
  in separate PRs.
- **Tests.** New behaviour needs a test; fixed bugs should get a
  regression test where practical.
- **Lint clean.** `make lint` must pass. CI enforces `gofmt`, `go
  vet`, and the security gates (`govulncheck`, `npm audit`,
  `gitleaks`).
- **No generated files.** Don't commit `go.sum` diffs that aren't
  needed, bundled JS output, or local `.env` files. `.gitignore`
  should already cover the common cases.
- **Commit messages.** Short subject line, imperative mood. Reference
  the issue number when one exists. The body can be as detailed as it
  needs to be.
- **Document operator-visible changes** in
  `server/docs/changelog.md`. Tooling / CI / internal refactors do
  not go in the changelog.

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

- Code without a license-compatible license (the project is MIT).
- Changes that remove or weaken the security gates in CI.
- Additions that bundle operator-specific secrets, hostnames, or
  network details into the shipped tree.

## Release cadence

Maintainers tag a release after merging changes they want to deploy.
There's no fixed schedule; releases are cut when they're ready.

## Questions

Open a discussion or a low-priority issue. Response times vary — this
is a side project, not a commercial product.
