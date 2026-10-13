param([string]$DevelopmentRoot = '', [int]$DurationSeconds = 0)
$ErrorActionPreference = 'Stop'
if ($DurationSeconds -lt 0) { throw 'DurationSeconds cannot be negative' }
$RepositoryRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($DevelopmentRoot)) {
    $DevelopmentRoot = Split-Path -Parent $RepositoryRoot
}
$DevelopmentRoot = [IO.Path]::GetFullPath($DevelopmentRoot)
$GoExecutable = Join-Path $DevelopmentRoot 'tools/go/bin/go.exe'
if (-not (Test-Path -LiteralPath $GoExecutable -PathType Leaf)) { throw 'Portable Go toolchain missing under tools/go/bin/go.exe' }
$RunRoot = Join-Path $DevelopmentRoot ('evidence/onboarding-server-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $RunRoot | Out-Null
$Identity = [Security.Principal.WindowsIdentity]::GetCurrent().User
$ACL = New-Object Security.AccessControl.DirectorySecurity
$ACL.SetOwner($Identity)
$ACL.SetAccessRuleProtection($true, $false)
foreach ($SID in @($Identity, (New-Object Security.Principal.SecurityIdentifier('S-1-5-18')), (New-Object Security.Principal.SecurityIdentifier('S-1-5-32-544')))) {
    $ACL.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($SID, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')))
}
Set-Acl -LiteralPath $RunRoot -AclObject $ACL
$Names = @('GOCACHE', 'GOMODCACHE', 'GOMAXPROCS', 'GOFLAGS', 'GOPROXY', 'DUMB_ADDR', 'DUMB_TOKENS_FILE', 'DUMB_ADMIN_ADDR', 'DUMB_MIN_WORKER_VERSION')
$Original = @{}
foreach ($Name in $Names) { $Original[$Name] = [Environment]::GetEnvironmentVariable($Name, 'Process') }
$Server = $null
$Listener = $null
$Client = $null
$Random = [Security.Cryptography.RandomNumberGenerator]::Create()
$Hash = [Security.Cryptography.SHA256]::Create()
try {
    $TokenBytes = New-Object byte[] 32
    $Random.GetBytes($TokenBytes)
    $WorkerToken = [Convert]::ToBase64String($TokenBytes)
    $Random.GetBytes($TokenBytes)
    $CallerToken = [Convert]::ToBase64String($TokenBytes)
    $Encoding = New-Object Text.UTF8Encoding($false)
    $WorkerFile = Join-Path $RunRoot 'worker.token'
    [IO.File]::WriteAllText($WorkerFile, $WorkerToken, $Encoding)
    [IO.File]::WriteAllText((Join-Path $RunRoot 'caller.token'), $CallerToken, $Encoding)
    $WorkerHash = ([BitConverter]::ToString($Hash.ComputeHash($Encoding.GetBytes($WorkerToken)))).Replace('-', '').ToLowerInvariant()
    $CallerHash = ([BitConverter]::ToString($Hash.ComputeHash($Encoding.GetBytes($CallerToken)))).Replace('-', '').ToLowerInvariant()
    $Table = Join-Path $RunRoot 'tokens.txt'
    [IO.File]::WriteAllText($Table, "onboarding-worker worker $WorkerHash`nonboarding-caller caller $CallerHash`n", $Encoding)
    $env:GOCACHE = Join-Path $DevelopmentRoot 'cache/go-build'
    $env:GOMODCACHE = Join-Path $DevelopmentRoot 'cache/go-mod'
    $env:GOMAXPROCS = '2'
    $env:GOFLAGS = '-p=1'
    $env:GOPROXY = 'off'
    $Binary = Join-Path $RunRoot 'dumb-server.exe'
    Push-Location $RepositoryRoot
    try {
        & $GoExecutable build -trimpath -o $Binary ./cmd/dumb-server
        if ($LASTEXITCODE -ne 0) { throw 'Local test server build failed' }
    } finally { Pop-Location }
    $Listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
    $Listener.Start()
    $Port = $Listener.LocalEndpoint.Port
    $Listener.Stop()
    $Listener = $null
    $URL = 'http://127.0.0.1:' + $Port
    $env:DUMB_ADDR = '127.0.0.1:' + $Port
    $env:DUMB_TOKENS_FILE = $Table
    $env:DUMB_ADMIN_ADDR = ''
    $env:DUMB_MIN_WORKER_VERSION = '0.0.0'
    $Server = Start-Process -FilePath $Binary -PassThru -RedirectStandardOutput (Join-Path $RunRoot 'server.stdout.log') -RedirectStandardError (Join-Path $RunRoot 'server.stderr.log')
    Add-Type -AssemblyName System.Net.Http
    $Client = New-Object Net.Http.HttpClient
    $Client.Timeout = [TimeSpan]::FromSeconds(2)
    $Client.DefaultRequestHeaders.Add('X-DUMB-Worker-Version', '0.0.0')
    $Client.DefaultRequestHeaders.Authorization = New-Object Net.Http.Headers.AuthenticationHeaderValue('Bearer', $WorkerToken)
    $Ready = $false
    $Deadline = [DateTime]::UtcNow.AddSeconds(15)
    while ([DateTime]::UtcNow -lt $Deadline) {
        if ($Server.HasExited) { throw 'Local test server exited before readiness; inspect its restricted log directory' }
        try {
            $Response = $Client.GetAsync($URL + '/v1/workers/me').GetAwaiter().GetResult()
            try {
                if ($Response.StatusCode -eq [Net.HttpStatusCode]::OK) {
                    $Body = $Response.Content.ReadAsStringAsync().GetAwaiter().GetResult() | ConvertFrom-Json
                    if ($Body.worker_id -eq 'onboarding-worker') { $Ready = $true; break }
                }
            } finally { $Response.Dispose() }
        } catch { }
        Start-Sleep -Milliseconds 100
    }
    if (-not $Ready) { throw 'Local test server did not confirm its worker credential' }
    Write-Output ('Server URL: ' + $URL)
    Write-Output ('Worker token file: ' + $WorkerFile)
    Write-Output 'Keep this terminal open. Read the token file privately and paste its contents into the worker setup token prompt.'
    Write-Output 'Stop this test server with Ctrl+C.'
    $Deadline = [DateTime]::MaxValue
    if ($DurationSeconds -gt 0) { $Deadline = [DateTime]::UtcNow.AddSeconds($DurationSeconds) }
    while ([DateTime]::UtcNow -lt $Deadline) {
        if ($Server.HasExited) { throw 'Local test server exited unexpectedly' }
        Start-Sleep -Milliseconds 100
    }
} finally {
    if ($Client) { $Client.Dispose() }
    if ($Listener) { $Listener.Stop() }
    if ($Server) {
        if (-not $Server.HasExited) { $Server.Kill(); $Server.WaitForExit() }
        $Server.Dispose()
    }
    $Hash.Dispose()
    $Random.Dispose()
    foreach ($Name in $Names) { [Environment]::SetEnvironmentVariable($Name, $Original[$Name], 'Process') }
}
