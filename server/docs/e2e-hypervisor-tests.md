---
title: Hypervisor e2e test battery
section: Reference
order: 70
---

# Hypervisor e2e test battery

`scripts/e2e-hypervisor.sh` is a self-contained bash test harness
that exercises every operator-facing surface in kvm-center against
one hypervisor. Designed to be:

- **rerunnable** — every test resource is named
  `e2eharness-<run-timestamp>-<scenario>` so concurrent runs and
  re-runs never collide
- **additive** — each scenario is one bash function + one
  `register` line; adding a new test is < 30 lines
- **operator-faithful** — every check goes through the
  kvm-center HTTP API the same way the SPA does (no agent SSH
  shortcuts for the operations under test; SSH is used only for
  diagnostic verify steps)
- **safe** — `--clean` sweeps leftover `e2eharness-*` resources
  without touching anything else; the destructive
  retire-and-re-enrol scenario is opt-in (`--with-retire`)

## Running

```sh
# Mint an admin API token from Settings → API Tokens, then:
export KVMC_TOKEN=kvmc_<id>.<secret>

# All non-destructive scenarios against hostD (the default target):
./scripts/e2e-hypervisor.sh

# Include the retire+re-enrol cycle (DESTRUCTIVE — wipes the
# host's enrolment, runs install.sh again, approves):
./scripts/e2e-hypervisor.sh --with-retire

# Just list scenarios:
./scripts/e2e-hypervisor.sh --list

# Run one scenario in isolation:
./scripts/e2e-hypervisor.sh --scenario D1

# Sweep any leftover resources from a previous aborted run:
./scripts/e2e-hypervisor.sh --clean

# Target a different host:
./scripts/e2e-hypervisor.sh --host hostB.example.com \
                           --peer hostA.example.com
```

Exit code is `0` if every selected scenario passed, non-zero on
any failure.

## What it covers

| ID | Surface |
|----|---------|
| A1 | Inventory: host appears in `/api/hosts` with mTLS + migrator-pubkey + ssh-host-keys all populated |
| A2 | Host detail has every required field (cpu / mem / agent_version etc.) |
| A3 | Telemetry freshness (`last_seen` < 60 s) |
| B1–B4 | Storage-pool lifecycle: define → visible → stop+start → undefine |
| C1 | Create a 256 MB / 1 vCPU / 1 GB Alpine VM through `/api/vm-create` |
| C2 | STOP / START / RESTART state transitions |
| C3 | **F1 regression**: 5xx with body `success:false` is terminal — VM not locked behind a "another action in flight" 409 |
| D1 | **v5.58.1 regression**: EDIT_VM with vcpus + mem_mb cap-raise + `allow_shutdown=true` runs ONE shutdown cycle, applies both live |
| N1 | Snapshots: create × 2, list, revert, delete × 2 |
| E1 | **v5.56.0**: expected-down set + inspect (reason + ETA + actor) + clear |
| F1 | Maintenance mode: enter blocks new-VM placement with 409, exit restores |
| G1 | Fleet mesh: `/api/agents/migrator-authorized-keys` + `/api/agents/ssh-known-hosts` both populated |
| G2 | Cross-host SSH from peer to target with `StrictHostKeyChecking=yes` succeeds (proves v5.55.4 + v5.56.1 mesh) |
| H1 | Live migrate the test VM off the target to the peer (auto transport, copy-storage) |
| I1 | Console token mint (token + exp + nonce + url fields) + HTML page render |
| J1 | `/api/admin/backup` returns a valid gzip tarball ≥ 10 KB |
| K1 | Host has an entry in the cert ledger with > 30 days expiry |
| L1 | `/api/audit` returns recent events |
| M1 | **--with-retire only**: full retire → deny-list → unretire → install.sh → approve → host re-online |

## Adding a new scenario

1. Write a `phase_<ID>_<short_name>` function. Return `0` on pass,
   `77` to skip, anything else to fail. Use `ok` / `bad` / `note`
   for narration.
2. Add a `register` line near the bottom — pick an ID, point at
   the function, write a one-sentence description.
3. If the scenario creates a VM, call `create_test_vm` so cleanup
   tracks it automatically. If it creates a pool, append the name
   to `CREATED_POOLS`.

That's it. The dispatcher picks it up on next run.

## Output

Each run writes a directory under `/tmp/kvmc-e2e-<pid>/` with:
- `run.log` — full narration
- `.body` — most recent HTTP response body
- `.vm_uuid` — UUID of the test VM (referenced across phases)

The dispatcher prints a coloured summary table at the end:

```
── summary ──
  PASS A1       0s  Inventory: /api/hosts lists the target …
  PASS A2       0s  Inventory: host detail has all required fields
  …
  pass=21  fail=0  skip=1  total=22
```

## CI hook

For an automated nightly run, a sketch:

```yaml
nightly-hypervisor-e2e:
  schedule:
    - cron: '0 4 * * *'
  steps:
    - run: scripts/e2e-hypervisor.sh
      env:
        KVMC_TOKEN: ${{ secrets.KVMC_E2E_TOKEN }}
        KVMC_SERVER: https://kvm.example.com:8190
        KVMC_HOST: hostD.example.com
        KVMC_PEER: hostA.example.com
```

The script's non-zero exit on failure gates the workflow naturally.
