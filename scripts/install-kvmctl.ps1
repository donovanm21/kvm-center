<#
.SYNOPSIS
  Installs the kvmctl CLI on Windows.

.DESCRIPTION
  Detects architecture (amd64 / arm64), downloads the matching binary
  from the kvm-center build portal, drops it into an install directory
  (default: %LOCALAPPDATA%\Programs\kvm-center), and adds that
  directory to the user's PATH.

.PARAMETER Portal
  https://HOST:8190/downloads base URL of the kvm-center build portal. Required.

.PARAMETER InstallDir
  Target directory. Defaults to %LOCALAPPDATA%\Programs\kvm-center.

.PARAMETER BinName
  Installed binary name (default: kvmctl.exe).

.PARAMETER Force
  Overwrite without prompting.

.EXAMPLE
  iwr https://kvm-center.example.com:8190/downloads/install-kvmctl.ps1 -UseBasicParsing | iex
    (then re-open PowerShell for the PATH update)

.EXAMPLE
  .\install-kvmctl.ps1 -Portal https://kvm-center.example.com:8190/downloads -InstallDir C:\tools
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$Portal,
    [string]$InstallDir,
    [string]$BinName = "kvmctl.exe",
    [switch]$Force
)

$ErrorActionPreference = 'Stop'

function Info($m) { Write-Host ">> $m" -ForegroundColor Cyan }
function OK  ($m) { Write-Host "OK $m"  -ForegroundColor Green }
function Die ($m) { Write-Host "ERROR: $m" -ForegroundColor Red; exit 1 }

$Portal = $Portal.TrimEnd('/')

# Detect arch.
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { Die "unsupported arch: $env:PROCESSOR_ARCHITECTURE" }
}
$asset = "kvmctl-windows-$arch.exe"
$url   = "$Portal/$asset"
Info "detected: windows/$arch → $asset"

if (-not $InstallDir) {
    $InstallDir = Join-Path $env:LOCALAPPDATA "Programs\kvm-center"
}
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$target = Join-Path $InstallDir $BinName

if ((Test-Path $target) -and (-not $Force)) {
    $ans = Read-Host "$target exists. Overwrite? [y/N]"
    if ($ans -notin @('y','Y','yes','YES')) { Die "aborted" }
}

Info "downloading $url"
if ($url -like 'https://*') {
    # curl.exe -k: Windows Schannel rejects self-signed certs before .NET's
    # ServerCertificateValidationCallback runs, so use curl.exe (shipped with
    # Windows 10 1803+ / Server 2019+) which trusts -k directly.
    & curl.exe -fsSLk $url -o $target --max-time 60
    if ($LASTEXITCODE -ne 0) { Die "curl.exe failed ($LASTEXITCODE) downloading $url" }
} else {
    try {
        Invoke-WebRequest -Uri $url -OutFile $target -UseBasicParsing -TimeoutSec 60
    } catch {
        Die "download failed: $($_.Exception.Message)"
    }
}
if ((Get-Item $target).Length -eq 0) { Die "downloaded file is empty" }
OK "installed $target"

# Add InstallDir to user PATH if not already present.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$InstallDir", 'User')
    Info "added $InstallDir to user PATH (open a new terminal for it to take effect)"
}

& $target version
Write-Host ""
Write-Host "Next: mint an API token in Settings → API Tokens, then log in:"
Write-Host "  $BinName login --server https://<kvm-center-host>:8190 --token kvmc_..."
