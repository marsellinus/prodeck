<#
.SYNOPSIS
    Build the host agent and run it against a throwaway configuration directory.

.DESCRIPTION
    The instance is bound to 127.0.0.1 so it is not reachable from the LAN, and
    every file it writes (config, devices, audit log, TLS material, profiles,
    logs) lives under .\.devdata, which is gitignored. Delete that directory to
    reset to a first-run state.

.PARAMETER HostArguments
    Extra global flags passed through to the host before the `run` subcommand.

.EXAMPLE
    scripts\dev-host.ps1
    scripts\dev-host.ps1 --allow-absolute-paths
#>
[CmdletBinding()]
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$HostArguments
)

$ErrorActionPreference = 'Stop'

# Resolve the repository root from this script's location, so the script works
# from any working directory.
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot  = (Resolve-Path (Join-Path $scriptDir '..')).Path

$hostDir = Join-Path $repoRoot 'host'
$devDir  = Join-Path $repoRoot '.devdata'
$binary  = Join-Path $hostDir 'mobiledeck.exe'

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'dev-host: go is not on PATH; install Go 1.26 or newer'
}

New-Item -ItemType Directory -Force -Path $devDir | Out-Null

Write-Host "dev-host: building host -> $binary"
Push-Location $hostDir
try {
    $env:CGO_ENABLED = '0'
    go build -trimpath -o mobiledeck.exe ./cmd/mobiledeck
    if ($LASTEXITCODE -ne 0) { throw "dev-host: go build failed ($LASTEXITCODE)" }
} finally {
    Pop-Location
}

Write-Host "dev-host: config dir $devDir"
Write-Host 'dev-host: bind 127.0.0.1 (loopback only; not reachable from a phone)'
Write-Host 'dev-host: stop with Ctrl+C'

$arguments = @('--config-dir', $devDir, '--bind', '127.0.0.1')
if ($HostArguments) {
    $arguments += $HostArguments
}
$arguments += 'run'

& $binary @arguments
exit $LASTEXITCODE
