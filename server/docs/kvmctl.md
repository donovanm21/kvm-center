---
title: kvmctl CLI
section: Reference
order: 60
---

# kvmctl CLI

Scriptable access to the kvm-center API. Authenticates via a bearer
token minted from the user menu (personal) or Settings → API Tokens
(system).

## Install

### Linux / macOS (one-shot)

```sh
curl -fsSLk https://<server>:8190/downloads/install-kvmctl.sh | bash -s -- \
  --portal https://<server>:8190/downloads
```

Auto-detects OS + arch, installs to `/usr/local/bin` (falls back to
`~/.local/bin` when not root), strips macOS quarantine. Flags:
`--install-dir`, `--bin-name`, `--force`.

### Windows PowerShell

```powershell
iwr https://<server>:8190/downloads/install-kvmctl.ps1 -UseBasicParsing -OutFile $env:TEMP\i.ps1
& $env:TEMP\i.ps1 -Portal https://<server>:8190/downloads
```

### Manual download

Binaries are served at `/downloads/`: `kvmctl-linux-amd64`,
`-linux-arm64`, `-darwin-amd64`, `-darwin-arm64`,
`-windows-amd64.exe`.

## Authentication

```sh
kvmctl login --server https://kvm.example.com:8190 --token kvmc_…
```

Config lands at `~/.config/kvm-center/config.json` (mode 0600).
Subsequent commands read it automatically. `kvmctl logout` removes it.

## Commands

### Auth

- `kvmctl login --server <url> --token <token>`
- `kvmctl whoami` — user, roles, effective permissions
- `kvmctl config [--show-token]` — saved config, token masked by default
- `kvmctl logout`

### Inventory

- `kvmctl hosts [--json]`
- `kvmctl vms [--host <id>] [--json]`

### VM lifecycle

- `kvmctl vm start <uuid>`
- `kvmctl vm stop <uuid>`
- `kvmctl vm restart <uuid>`
- `kvmctl vm forcestop <uuid>`
- `kvmctl vm migrate <uuid> --to <host_id> [--mode live|offline]`

### Snapshots

- `kvmctl snapshot list <uuid>`
- `kvmctl snapshot create <uuid> [--name <name>]` — auto-generated
  timestamp name when omitted.

### Misc

- `kvmctl version`
- `kvmctl help`

## Output formatting

Human-readable by default. Append `--json` to inventory commands for
machine-parseable output. Set `NO_COLOR=1` to disable the help
screen's ANSI escapes.

## Audit attribution

Every action `kvmctl` takes is audited with `detail.via=token:<id>`
on the server side. The Audit Log's Via column shows **CLI** for
token-authenticated actions.
