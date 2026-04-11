# install.ps1 — one-shot installer for kvm-tools on a Windows guest VM.
#
# Installs C:\Program Files\kvm-tools\kvm-tools.exe and registers it as a
# Scheduled Task that runs at boot under the SYSTEM account, reporting guest
# telemetry (OS, kernel, IPs, filesystems) to the kvm-center server.
#
# Usage (run from an elevated PowerShell prompt):
#   .\install.ps1 -Server http://10.10.100.8:8090
#
# Or one-shot from the deployment portal:
#   iwr -useb http://10.10.100.8:8097/install-kvm-tools.ps1 | iex
# (when invoked that way the script will fetch kvm-tools.exe from the same
#  portal automatically)

[CmdletBinding()]
param(
    [string]$Server   = $env:KVM_TOOLS_SERVER,
    [string]$Interval = "30s",
    [string]$BinaryUrl = ""
)

if (-not $Server) { $Server = "http://10.10.100.8:8090" }

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
        # Derive download URL from $Server: replace 8090 with 8097 (deployment portal)
        $portal = $Server -replace ":8090.*$", ":8097"
        $BinaryUrl = "$portal/kvm-tools-windows-amd64.exe"
    }
    Write-Info "Downloading $BinaryUrl ..."
    $tmp = Join-Path $env:TEMP "kvm-tools.exe"
    try {
        Invoke-WebRequest -Uri $BinaryUrl -OutFile $tmp -UseBasicParsing
    } catch {
        Fail "Could not download kvm-tools.exe from $BinaryUrl : $_"
    }
    $Source = $tmp
}
Write-Info "Using binary: $Source"

# Install dir + binary
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir | Out-Null
}
Copy-Item -Force $Source $BinPath
Write-Info "Binary installed to $BinPath"

# Sanity check the DMI UUID is reachable
try {
    $uuid = (Get-CimInstance Win32_ComputerSystemProduct).UUID
    Write-Info "Detected VM UUID: $uuid"
} catch {
    Write-Warn "Cannot read VM UUID via CIM — kvm-tools may fail to start"
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
    -Description "KVM Center Guest Tools — reports guest telemetry to kvm-center" | Out-Null
Write-Info "Scheduled task '$TaskName' registered (runs as SYSTEM at boot)"

Start-ScheduledTask -TaskName $TaskName
Start-Sleep -Seconds 2

$state = (Get-ScheduledTask -TaskName $TaskName).State
$lastResult = (Get-ScheduledTaskInfo -TaskName $TaskName).LastTaskResult
if ($state -eq "Running" -or $lastResult -eq 0 -or $lastResult -eq 267009) {
    Write-Info "kvm-tools is running (state=$state, lastResult=$lastResult)"
    Write-Info "Verify in kvm-center: http://10.10.100.8:8096"
} else {
    Write-Warn "kvm-tools task state=$state lastResult=$lastResult — check Task Scheduler for details"
}
