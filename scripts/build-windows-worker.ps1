param([string]$Output = "dist/windows", [string]$Version = "0.0.0")
$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$OriginalCGO = $env:CGO_ENABLED
$OriginalGOOS = $env:GOOS
$OriginalGOARCH = $env:GOARCH
Push-Location $Root
try {
    if ($Version -notmatch '^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') { throw "Invalid release version" }
    go run ./scripts/windows-resources -version $Version
    if ($LASTEXITCODE -ne 0) { throw "Windows resource generation failed" }
    New-Item -ItemType Directory -Force -Path $Output | Out-Null
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $WorkerLDFlags = "-H windowsgui -X main.version=$Version"
    if ($env:DUMB_RELEASE_PUBLIC_KEY) {
        $WorkerLDFlags += " -X github.com/gkhntpbs/dumb/internal/update.PublicKey=$env:DUMB_RELEASE_PUBLIC_KEY"
    }
    go build -trimpath -ldflags $WorkerLDFlags -o "$Output/dumb-worker.exe" ./cmd/dumb-worker
    if ($LASTEXITCODE -ne 0) { throw "Worker build failed" }
    go build -trimpath -ldflags "-H windowsgui" -o "$Output/dumb-tray.exe" ./cmd/dumb-tray
    if ($LASTEXITCODE -ne 0) { throw "Tray build failed" }
    Write-Output "Self-contained desktop worker: $Output/dumb-worker.exe; optional compatibility helper: $Output/dumb-tray.exe"
} finally {
    $env:CGO_ENABLED = $OriginalCGO
    $env:GOOS = $OriginalGOOS
    $env:GOARCH = $OriginalGOARCH
    Pop-Location
}
