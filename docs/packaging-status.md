# Packaging inputs and acceptance

The public Docker build includes the agent updater, Linux/macOS and Windows CLI
installers, and libvirt migration hook as tracked source files. Docker uses
locked npm dependencies, builds/tests the SPA, and checks its index before
embedding it. Server and guest tools stamp `versionRaw`; agents stamp `Version`;
the CLI stamps `cliVersion`. Pass the exact source revision as `GIT_SHA` to stamp
all binaries; an omitted Docker build argument is explicitly `dev`.

For a non-publishing native build, use `scripts/build-package.sh OUTPUT_DIR`.
It always rebuilds the SPA before calling `scripts/build-release.sh`. Neither
script installs, deploys or publishes. The tag-release workflow has not been
changed: it still needs separate adoption of the fresh-SPA build and explicit
SHA stamping before its outputs can receive release acceptance.

The restored updater touches only the selected agent binary and service. It
stages beside the target, checks the candidate's version, retains `.previous`,
and attempts restoration/restart on failure or interruption. A failed update
still exits nonzero; rollback health must be inspected. An existing `.previous`
requires operator resolution before another update. It does not install host
packages, rewrite endpoint URLs, adjust libvirt or change firewall rules.

Downloads verify TLS by default. An operator who deliberately accepts an
unverified certificate can use the standalone updater/CLI installer's explicit
`--insecure` option (`-Insecure` in PowerShell). `bootstrap --update-agents` does
not forward this option: its downloads require a certificate trusted by the
host. This differs from the preserved local scripts' unconditional TLS bypass.
Windows CLI packaging supports amd64 only. PowerShell execution and native
Windows/macOS installation still require target-platform acceptance.

The migration hook runs only for `migrate/begin`; ordinary `prepare/begin`
starts are unaffected. It makes one bounded local request, URL-encodes the VM
name and emits no stdout. Missing grants (403) reject migration. Transport
failures and server errors retain the explicitly documented legacy fail-open
availability policy: this hook alone is not an authorization boundary.
Existing hooks, including dangling symlinks, are preserved. Installing a new
hook does not activate it by reload; schedule any required libvirt restart
separately. No native hook or migration acceptance is claimed by mocked tests.

Offline Python regressions exercise the Docker command arguments with a fake
compiler, script downloads/services with mocks, update rollback/interruption,
hook event/status behavior, and fresh-SPA build ordering. They do not establish
that the complete image builds/runs, that downloads install on each OS, or that
an operational upgrade/rollback succeeds. Exact Docker artifact, native runtime,
history resource/recovery, deployment compatibility and remaining release
security acceptance are still required. No release or deployment occurs here.
