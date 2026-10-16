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

# Against an authorized disposable remote demo at a known revision
KVMC_EXPECTED_SHA=<40-character-deployed-SHA> KVMC_EXPECTED_BACKEND=mongo \
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

## Manual isolated native fixture

The manual fixture builds a local server and seeds a random disposable
MongoDB with outbound notifications and scheduled mutations disabled. It runs
Chromium + iPhone/WebKit assertions with zero retries and checks the server's
Git SHA. This is real browser/API/Mongo coverage with synthetic inventory;
it is not real-hypervisor validation.

```sh
npm ci --prefix demo/e2e
cd demo/e2e && npx --no-install playwright install chromium webkit && cd ../..
KVMC_TEST_MONGO_URI=mongodb://127.0.0.1:27017 scripts/run-browser-fixture.sh
```

For existing demo runs, set `KVMC_EXPECTED_SHA` to the exact deployed server
revision (defaults to the current checkout), and set `KVMC_EXPECTED_BACKEND=json`
for a JSON-backed demo (default: `mongo`). Both Docker paths forward these values;
the revision assertion fails if the deployment differs. See
[validation gates and feature matrix](../../docs/validation.md).

The approved `browser-integration` GitHub job now runs this isolated fixture
against its own disposable MongoDB 7 service, including Chromium and iPhone/WebKit.
It checks the exact candidate, uses zero retries, verifies cleanup and retains only
allowlisted reports for seven days. No production credentials or hypervisor are used.
See the validation document for timeouts, evidence limits and remaining release gates.
