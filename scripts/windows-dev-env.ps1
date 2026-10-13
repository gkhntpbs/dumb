# Dot-source this script to configure only the current PowerShell process.
# Place a checksum-verified portable Go toolchain beside the isolated core repo:
# <development root>/tools/go/bin/go.exe. No global installation or reboot.
param([string]$ToolchainRoot = "", [switch]$Offline)
$ErrorActionPreference = "Stop"
$RepositoryRoot = Split-Path -Parent $PSScriptRoot
$DevelopmentRoot = Split-Path -Parent $RepositoryRoot
if ([string]::IsNullOrWhiteSpace($ToolchainRoot)) {
    $ToolchainRoot = Join-Path $DevelopmentRoot 'tools/go'
}
$GoExecutable = Join-Path $ToolchainRoot 'bin/go.exe'
if (-not (Test-Path -LiteralPath $GoExecutable -PathType Leaf)) {
    throw 'Portable Go toolchain missing. Supply -ToolchainRoot or place it under the development root tools/go directory.'
}
$env:PATH = (Join-Path $ToolchainRoot 'bin') + ';' + $env:PATH
$env:GOCACHE = Join-Path $DevelopmentRoot 'cache/go-build'
$env:GOMODCACHE = Join-Path $DevelopmentRoot 'cache/go-mod'
$env:GOMAXPROCS = '2'
$env:GOFLAGS = '-p=1'
if ($Offline) { $env:GOPROXY = 'off' }
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOMODCACHE | Out-Null
& $GoExecutable version
if ($LASTEXITCODE -ne 0) { throw 'Portable Go toolchain could not run' }
Write-Output 'Development environment configured for this PowerShell process only.'
