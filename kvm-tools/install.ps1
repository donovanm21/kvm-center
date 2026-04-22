# install.ps1 - one-shot installer for kvm-tools on a Windows guest VM.
# Installs C:\Program Files\kvm-tools\kvm-tools.exe as a Scheduled Task
# running as SYSTEM at boot.
#
# Usage (elevated PowerShell):
#   .\install.ps1 -Server https://kvm-center.example.com:8190
# Self-signed certs are trusted; install a real cert via Settings → TLS
# to silence the per-report warning.

[CmdletBinding()]
param(
    [string]$Server   = $env:KVM_TOOLS_SERVER,
    [string]$Interval = "30s",
    [string]$BinaryUrl = ""
)

if (-not $Server) { $Server = "https://localhost:8190" }

$ErrorActionPreference = "Stop"
$InstallDir = "C:\Program Files\kvm-tools"
$BinPath    = Join-Path $InstallDir "kvm-tools.exe"
$TaskName   = "kvm-tools"

function Write-Info($msg) { Write-Host "[OK] $msg" -ForegroundColor Green }
function Write-Warn($msg) { Write-Host "[!] $msg"  -ForegroundColor Yellow }
function Fail($msg)       { Write-Host "[X] $msg"  -ForegroundColor Red; exit 1 }

# Must be Administrator
$current = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $current.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail "This script must be run from an elevated PowerShell prompt (Run as Administrator)."
}

# Resolve binary source
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
$LocalBin  = Join-Path $ScriptDir "kvm-tools.exe"
$LocalBinAlt = Join-Path $ScriptDir "kvm-tools-windows-amd64.exe"

if (Test-Path $LocalBin) {
    $Source = $LocalBin
} elseif (Test-Path $LocalBinAlt) {
    $Source = $LocalBinAlt
} else {
    if (-not $BinaryUrl) {
        # v5.16+: downloads live on the server itself under /downloads/.
        $BinaryUrl = "$($Server.TrimEnd('/'))/downloads/kvm-tools-windows-amd64.exe"
    }
    Write-Info "Downloading $BinaryUrl ..."
    $tmp = Join-Path $env:TEMP "kvm-tools.exe"
    # curl.exe -k bypasses Schannel's cert-chain check entirely, which
    # .NET's ServerCertificateValidationCallback can't reliably do for
    # self-signed servers on Windows. Requires Windows 10 1803+.
    if ($BinaryUrl -like 'https://*') {
        Write-Warn "TLS certificate verification disabled (self-signed CA tolerated via curl -k)."
        & curl.exe -fsSLk $BinaryUrl -o $tmp
        if ($LASTEXITCODE -ne 0) { Fail "curl.exe failed ($LASTEXITCODE) downloading $BinaryUrl" }
    } else {
        try {
            Invoke-WebRequest -Uri $BinaryUrl -OutFile $tmp -UseBasicParsing
        } catch {
            Fail "Could not download kvm-tools.exe from $BinaryUrl : $_"
        }
    }
    $Source = $tmp
}
Write-Info "Using binary: $Source"

# Install dir + binary
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir | Out-Null
}

# Re-install / upgrade path (v5.33.5): Copy-Item -Force can't
# overwrite a running kvm-tools.exe — Windows holds the file open as
# long as the scheduled task is active. But NTFS *does* let us
# rename a running exe to a different name within the same
# directory. So:
#   1. Stop the scheduled task (best-effort; also releases the file
#      handle on success).
#   2. If the target still exists, rename it to kvm-tools.exe.old.
#   3. Copy the new binary into place.
#   4. Restart the scheduled task below.
if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
    try {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        # Give Task Scheduler a moment to actually release the handle.
        Start-Sleep -Seconds 1
    } catch {
        Write-Warn "Could not stop scheduled task cleanly — will rely on the rename fallback"
    }
}

if (Test-Path $BinPath) {
    $BackupPath = "$BinPath.old"
    Remove-Item -Force -ErrorAction SilentlyContinue $BackupPath
    try {
        # Rename is permitted on Windows even if the exe is still
        # running. Copy-Item -Force is not. This is the key fix for
        # the 'process cannot access the file' error.
        Rename-Item -Force -ErrorAction Stop $BinPath $BackupPath
    } catch {
        Fail "Could not rename existing $BinPath out of the way: $_"
    }
}

try {
    Copy-Item -Force -ErrorAction Stop $Source $BinPath
} catch {
    # Roll back so the host isn't left without any binary.
    if (Test-Path "$BinPath.old") {
        Rename-Item -Force "$BinPath.old" $BinPath -ErrorAction SilentlyContinue
    }
    Fail "Copy of new binary failed: $_"
}
Write-Info "Binary installed to $BinPath"

# Sanity check the DMI UUID is reachable
try {
    $uuid = (Get-CimInstance Win32_ComputerSystemProduct).UUID
    Write-Info "Detected VM UUID: $uuid"
} catch {
    Write-Warn "Cannot read VM UUID via CIM - kvm-tools may fail to start"
}

# Remove any existing task before re-registering
if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    Write-Info "Removed existing scheduled task"
}

$action    = New-ScheduledTaskAction -Execute $BinPath -Argument "-server $Server -interval $Interval"
$trigger   = New-ScheduledTaskTrigger -AtStartup
$principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -RunLevel Highest -LogonType ServiceAccount
$settings  = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -RestartCount 99 `
    -RestartInterval (New-TimeSpan -Minutes 1)

Register-ScheduledTask -TaskName $TaskName `
    -Action $action `
    -Trigger $trigger `
    -Principal $principal `
    -Settings $settings `
    -Description "KVM Center Guest Tools - reports guest telemetry to kvm-center" | Out-Null
Write-Info "Scheduled task '$TaskName' registered (runs as SYSTEM at boot)"

Start-ScheduledTask -TaskName $TaskName
Start-Sleep -Seconds 2

$state = (Get-ScheduledTask -TaskName $TaskName).State
$lastResult = (Get-ScheduledTaskInfo -TaskName $TaskName).LastTaskResult
if ($state -eq "Running" -or $lastResult -eq 0 -or $lastResult -eq 267009) {
    Write-Info "kvm-tools is running (state=$state, lastResult=$lastResult)"
    Write-Info "Verify in kvm-center: $Server"
} else {
    Write-Warn "kvm-tools task state=$state lastResult=$lastResult - check Task Scheduler for details"
}
