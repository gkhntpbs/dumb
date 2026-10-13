# Opt-in native acceptance. No publishing, reboot or existing-service changes.
param(
    [switch]$Update,
    [switch]$SCM,
    [switch]$SCMUpdate,
    [switch]$Install,
    [switch]$InstalledRun,
    [string]$OllamaDataDir = '',
    [string]$OfficialArchive = '',
    [string]$OfficialArchiveSHA256 = ''
)
$ErrorActionPreference = 'Stop'
if (-not ($Update -or $SCM -or $SCMUpdate -or $Install -or $InstalledRun)) { throw 'Select at least one explicit acceptance case.' }
if (($Install -or $InstalledRun) -and -not [IO.Path]::IsPathRooted($OllamaDataDir)) { throw 'Real Ollama cases require an absolute, isolated owned data directory.' }
if ($OfficialArchive -and $OfficialArchiveSHA256 -notmatch '^[a-fA-F0-9]{64}$') { throw 'A local official archive requires its trusted SHA256 digest.' }
$Root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$Keys = @('QA_WINDOWS_UPDATE','QA_WINDOWS_UPDATE_SCM','QA_WINDOWS_SCM','QA_WINDOWS_OLLAMA','QA_WINDOWS_OLLAMA_RUN','DUMB_OLLAMA_DATA_DIR','DUMB_OLLAMA_INSTALL_ARCHIVE','DUMB_OLLAMA_INSTALL_SHA256','GOMAXPROCS','GOFLAGS')
$Original = @{}
foreach ($Key in $Keys) { $Original[$Key] = [Environment]::GetEnvironmentVariable($Key,'Process') }
function Run-Case([string]$Pattern,[string]$Package,[string]$Timeout) {
    go test -count=1 -v -timeout $Timeout -run $Pattern $Package
    if ($LASTEXITCODE -ne 0) { throw ('Native acceptance failed: ' + $Pattern) }
}
Push-Location $Root
try {
    $env:GOMAXPROCS = '2'; $env:GOFLAGS = '-p=1'
    if ($Update) { $env:QA_WINDOWS_UPDATE = '1'; Run-Case 'TestSignedWindowsWorkerLifecycle' './internal/update/e2e/windowse2e' '5m' }
    if ($SCMUpdate) { $env:QA_WINDOWS_UPDATE_SCM = '1'; Run-Case 'TestSignedWindowsSCMUpdateAndRecovery' './internal/update/e2e/windowse2e' '5m' }
    if ($SCM) { $env:QA_WINDOWS_SCM = '1'; Run-Case 'TestWindowsSCMWorker' './internal/service/e2e/windows' '5m' }
    if ($Install -or $InstalledRun) {
        $env:DUMB_OLLAMA_DATA_DIR = $OllamaDataDir
        $env:DUMB_OLLAMA_INSTALL_ARCHIVE = $OfficialArchive
        $env:DUMB_OLLAMA_INSTALL_SHA256 = $OfficialArchiveSHA256
    }
    if ($Install) {
        $env:QA_WINDOWS_OLLAMA = '1'
        Run-Case 'TestWindowsOfficialHTTPS' './internal/ollama/e2e/windows' '2m'
        Run-Case 'TestOfficialWindowsOllamaLifecycle' './internal/ollama/e2e/windows' '25m'
    }
    if ($InstalledRun) {
        $env:QA_WINDOWS_OLLAMA_RUN = '1'
        Run-Case 'TestActualWindowsWorkerWithInstalledOllama' './internal/ollama/e2e/windows' '5m'
    }
} finally {
    foreach ($Key in $Keys) { [Environment]::SetEnvironmentVariable($Key,$Original[$Key],'Process') }
    Pop-Location
}
