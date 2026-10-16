# Container health probe process lifecycle

The server's Compose healthcheck previously invoked a command-line HTTPS client
that could use external TLS helper processes. The server is not a general child
reaper. Repeated healthchecks must not leave helper descendants behind, including
when a request fails or times out.

Server services in the full, server-only and demo Compose configurations now use
`init: true`. Docker's init process forwards signals and reaps adopted children.
The healthcheck directly executes `/app/kvm-healthcheck`, a small Go program that
performs TLS in-process without spawning a shell or helper. The image builder
cross-compiles the probe for the same target as the server.

The probe only accepts a literal-loopback HTTPS `/health` target, disables
environment proxies and redirects, requires HTTP 200, limits response bytes, and
uses an overall three-second timeout inside Docker's five-second healthcheck
budget. Connections and response bodies are closed on every outcome. The
self-signed certificate exception is limited to this loopback liveness check,
matching the old healthcheck; it does not change application TLS or trust policy.
No seccomp or kernel protections are altered.

Docker marks a container unhealthy after repeated failures; a restart policy does
not itself restart a running but unhealthy container. Recovery is an explicit
operational action, not a side effect of this healthcheck.

## Evidence and limits

Focused tests repeatedly exercise success, non-200 responses, redirect refusal,
connect failure, header/body timeout, response-size limits and cancellation. They
verify that fixture connections close. The probe imports no subprocess API.
These tests do not prove behavior of every Docker/runtime/init combination.
Production-image build, container startup/reaping/termination and target-OS
acceptance remain required before release. Existing absent installer/package
inputs must be resolved through their own reviewed work; do not substitute
unreviewed files or weaken release gates to build an image.

## Upgrade and rollback

This source change does not fix an already exhausted host or remove existing
processes. Do not restart a live service until an approved backup/restore plan,
retained previous immutable image, configuration and health baseline are verified.
Use a scoped updater targeting one explicit Compose project and agent allowlist;
never reset a moving branch, globally prune images/caches or roll every known
agent as an implicit side effect. Preserve prior image digests/configuration and
rollback evidence. The code can be reverted independently; any runtime update
and rollback must be performed by the separately authorized deployment workflow.
