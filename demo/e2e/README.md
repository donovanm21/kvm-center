# Tier 2 E2E (Playwright)

Assertion-driven SPA tests against a running demo deployment.

Sister to `demo/walkthroughs/`, which produces silent recording
clips. The walkthroughs *navigate* — these *assert*. A failing E2E
run means the SPA broke; a failing walkthrough means the recording
is wrong.

## Run

```sh
# Default — local demo on port 8290
scripts/run-e2e.sh

# Against a remote demo
KVMC_DEMO_URL=https://10.0.0.1:8290 scripts/run-e2e.sh

# One spec
scripts/run-e2e.sh specs/auth.spec.ts
```

The script is fully containerised. Failures land trace + video +
screenshot under `demo/e2e/output/<test-id>/` for post-mortem.

## What's covered

- **auth** — login happy path, bad password rejection, sign-out via
  the user menu drops back to the login form.
- **inventory** — dashboard host cards, VMs list with state pills,
  opening a VM detail page, hosts list with status pills. Also
  asserts the v5.37.0 `<StatePill>` aria-labels are wired (so a
  styling regression that drops them surfaces here).
- **system-health** — page heading + at least one labelled
  subsystem panel renders within the polling tick.
- **docs** — sidebar tree + rendered article; embedded WebP media
  endpoint returns 200 + image/webp content-type.

## Why this isn't in CI yet

Needs a demo backend. Adding to CI means standing up the demo
stack inside the runner (`docker compose up` + seed) before each
test pass. Future work — the suite itself is structured to make
that straightforward.
