---
title: Guest tools (kvm-tools)
section: Operations
order: 64
---

# Guest tools (`kvm-tools`)

`kvm-tools` is the small daemon that runs **inside guest VMs** and
reports back to kvm-center: guest OS, hostname, kernel, uptime,
IP addresses per interface, filesystem usage, and (optionally)
the kvm-tools version itself for the auto-update path.

It's optional — without it, kvm-center still tracks the libvirt-
visible state (running / stopped, vCPU, memory). With it, the
WebUI fills in the *Guest* block on each VM detail page.

## Install — Linux guests

systemd-managed; one-liner:

```sh
curl -fsSL https://<your-server>:8190/downloads/install-kvm-tools.sh | \
  sudo bash -s -- --server https://<your-server>:8190
```

The script downloads the `kvm-tools-linux-amd64` binary, drops it
at `/usr/local/bin/kvm-tools`, registers a `systemd` unit with
`Restart=always`, and starts it. Self-update is straightforward —
new binary written, `os.Exit(0)`, systemd brings it back up.

## Install — Windows guests

Windows can't `rename(2)`-over a running exe the way Linux can,
and there's no clean systemd analogue, so the install pattern is
slightly different. PowerShell as Administrator:

```powershell
[Net.ServicePointManager]::ServerCertificateValidationCallback = {$true}
$dest = "$env:TEMP\kvm-tools-amd64.exe"
Invoke-WebRequest -UseBasicParsing -Uri "https://<your-server>:8190/downloads/kvm-tools-windows-amd64.exe" -OutFile $dest
Invoke-WebRequest -UseBasicParsing -Uri "https://<your-server>:8190/downloads/install-kvm-tools.ps1" -OutFile "$env:TEMP\install-kvm-tools.ps1"
& "$env:TEMP\install-kvm-tools.ps1" -Source $dest -Server "https://<your-server>:8190"
```

The PowerShell installer:

1. Stops the existing `kvm-tools` scheduled task (if present).
2. Renames the existing `C:\Program Files\kvm-tools\kvm-tools.exe`
   to `kvm-tools.exe.old` (Windows allows renaming a running exe).
3. Copies the new binary into place.
4. Registers / re-registers the **scheduled task** with two
   triggers (v5.46.6+):
   - `AtStartup` — boot the daemon at machine start.
   - `Once / RepetitionInterval = 1 minute / Duration = indefinite`
     — a 60 s watchdog so Task Scheduler always re-fires the
     action within a minute of a clean self-update exit.

   `MultipleInstances = IgnoreNew`, so the watchdog is a benign
   no-op while kvm-tools is alive.
5. Starts the task as `SYSTEM` with `RunLevel = Highest`.

## Self-update behaviour

The server publishes the canonical kvm-tools binary at
`/downloads/kvm-tools-{linux,windows}-{amd64,arm64}{,.exe}`. On
each telemetry tick, the agent compares its embedded version
string to the server's; if newer, it downloads, swaps, and
relaunches.

- **Linux:** rename-over works; `os.Exit(0)` and systemd
  re-spawns. Sub-second downtime.
- **Windows:** rename-trick (running exe → `.exe.old`, new exe
  in place), `os.Exit(0)`, then within ≤ 60 s the watchdog
  trigger fires and the new binary boots. Single-minute downtime.

## Troubleshooting

### Linux: agent stays on the old version after a kvm-center upgrade

The telemetry tick is 30 s by default; downloads take a few
seconds; systemd restart adds another 1–2 s. Wait a minute. If it
still hasn't rotated, check `journalctl -u kvm-tools -n 50` for
download or signature errors.

### Windows: agent stuck on the old version, task State is Ready

This was the long-running v5.33.5 → v5.46.4 saga. The fix landed
in v5.46.6 — a watchdog trigger that re-fires the action every
minute. If you're seeing the symptom now:

1. Verify the task has the watchdog trigger:

   ```powershell
   (Get-ScheduledTask kvm-tools).Triggers | Format-List
   ```

   You should see two entries — one `AtStartup`, one with a
   `Repetition` whose `Interval` is `PT1M`.

2. If only `AtStartup` is present, you're on a pre-v5.46.6
   `install.ps1`. Re-run the PowerShell installer block above
   to register the new triggers.

3. Confirm `kvm-tools` actually starts: `Get-Process kvm-tools`.
   If absent, manually fire it once with `Start-ScheduledTask
   -TaskName kvm-tools` and watch for it in the process list.

### Windows: server logs full of `tls: unknown certificate` from a guest IP

That's not kvm-tools (it sets `InsecureSkipVerify: true` and
trusts any server cert). It's almost always a stale **browser
tab** or **PWA install** on the guest still pointing at
`https://<server>:8190` with a previously-cached self-signed
cert that no longer matches after the server rebuilt. Closing
the browser tab / re-accepting the cert silences it.

### Windows: `Copy-Item: process cannot access the file`

Pre-v5.33.5 install scripts copied over a running `kvm-tools.exe`,
which Windows blocks. Modern `install.ps1` does a Stop-Task →
rename → copy dance. Make sure you're using the latest installer
from the server (`<server>/downloads/install-kvm-tools.ps1`), not
a stashed local copy.

## Uninstall

Linux:

```sh
sudo systemctl disable --now kvm-tools
sudo rm -f /usr/local/bin/kvm-tools /etc/systemd/system/kvm-tools.service
sudo systemctl daemon-reload
```

Windows (PowerShell as Administrator):

```powershell
Stop-ScheduledTask -TaskName kvm-tools -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName kvm-tools -Confirm:$false
Remove-Item -Recurse -Force "C:\Program Files\kvm-tools"
```
