param([Parameter(Mandatory=$true)][string]$Executable, [Parameter(Mandatory=$true)][int]$SessionId, [string]$OwnedOllamaRoot = '', [string]$EvidenceRoot = '', [switch]$StartupCommand, [switch]$ClearOllamaEnvironment)
$ErrorActionPreference = 'Stop'
$Executable = [IO.Path]::GetFullPath($Executable)
if (-not (Test-Path -LiteralPath $Executable -PathType Leaf)) { throw 'Executable is missing' }
if ($SessionId -le 0) { throw 'An existing interactive session is required' }
if ($OwnedOllamaRoot -and -not [IO.Path]::IsPathRooted($OwnedOllamaRoot)) { throw 'Owned Ollama root must be absolute' }
$Identity = [Security.Principal.WindowsIdentity]::GetCurrent().User
$Explorer = @(Get-CimInstance Win32_Process -Filter "Name='explorer.exe'" | Where-Object { $_.SessionId -eq $SessionId } | Where-Object { (Invoke-CimMethod -InputObject $_ -MethodName GetOwnerSid).Sid -eq $Identity.Value })
if ($Explorer.Count -ne 1) { throw 'Exactly one same-user Explorer is required in the selected existing session' }
$Nonce = [guid]::NewGuid().ToString('N')
$TaskName = 'DUMB-QA-Interactive-' + $Nonce
if (-not $EvidenceRoot) { $EvidenceRoot = Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) ('evidence/interactive-' + $Nonce) }
$EvidenceRoot = [IO.Path]::GetFullPath($EvidenceRoot)
New-Item -ItemType Directory -Path $EvidenceRoot | Out-Null
$ACL = New-Object Security.AccessControl.DirectorySecurity
$ACL.SetOwner($Identity)
$ACL.SetAccessRuleProtection($true,$false)
foreach ($SID in @($Identity,(New-Object Security.Principal.SecurityIdentifier('S-1-5-18')),(New-Object Security.Principal.SecurityIdentifier('S-1-5-32-544')))) {
    $ACL.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($SID,'FullControl','ContainerInherit,ObjectInherit','None','Allow')))
}
Set-Acl -LiteralPath $EvidenceRoot -AclObject $ACL
$Receipt = Join-Path $EvidenceRoot 'launch.json'
$QuotedExe = $Executable.Replace("'","''")
$QuotedReceipt = $Receipt.Replace("'","''")
$Launch = ''
if ($ClearOllamaEnvironment) { $Launch += 'Remove-Item Env:DUMB_OLLAMA_DATA_DIR,Env:OLLAMA_MODELS,Env:DUMB_OLLAMA -ErrorAction SilentlyContinue; ' }
if ($OwnedOllamaRoot) { $Launch += '$env:DUMB_OLLAMA_DATA_DIR=''' + $OwnedOllamaRoot.Replace("'","''") + '''; ' }
$Arguments = ''
if ($StartupCommand) { $Arguments = ' -ArgumentList ''desktop''' }
$Launch += '$process=Start-Process -FilePath ''' + $QuotedExe + '''' + $Arguments + ' -PassThru; [IO.File]::WriteAllText(''' + $QuotedReceipt + ''',(($process | Select-Object Id,SessionId) | ConvertTo-Json))'
$Encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($Launch))
$Scheduler = New-Object -ComObject Schedule.Service
$Scheduler.Connect()
$Folder = $Scheduler.GetFolder('\')
$Definition = $Scheduler.NewTask(0)
$Definition.RegistrationInfo.Description = 'Temporary explicitly authorized DUMB interactive QA launch'
$Definition.Principal.UserId = $Identity.Value
$Definition.Principal.LogonType = 3
$Definition.Principal.RunLevel = 0
$Definition.Settings.AllowDemandStart = $true
$Definition.Settings.DisallowStartIfOnBatteries = $false
$Definition.Settings.StopIfGoingOnBatteries = $false
$Definition.Settings.ExecutionTimeLimit = 'PT1M'
$Action = $Definition.Actions.Create(0)
$Action.Path = Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
$Action.Arguments = '-NoProfile -WindowStyle Hidden -EncodedCommand ' + $Encoded
$Action.WorkingDirectory = Split-Path -Parent $Executable
$Registered = $false
try {
    $Task = $Folder.RegisterTaskDefinition($TaskName,$Definition,2,$Identity.Value,$null,3,$null)
    $Registered = $true
    $Task.Run($null) | Out-Null
    $Deadline = [DateTime]::UtcNow.AddSeconds(20)
    while (-not (Test-Path -LiteralPath $Receipt) -and [DateTime]::UtcNow -lt $Deadline) { Start-Sleep -Milliseconds 100 }
    if (-not (Test-Path -LiteralPath $Receipt)) { throw ('Interactive launch did not produce a receipt; task result=' + $Task.LastTaskResult) }
    $Started = Get-Content -Raw -LiteralPath $Receipt | ConvertFrom-Json
    if ($Started.SessionId -ne $SessionId) { throw 'Owned launch did not enter the selected session' }
    $Process = Get-CimInstance Win32_Process -Filter ('ProcessId=' + $Started.Id)
    if (-not $Process -or (Invoke-CimMethod -InputObject $Process -MethodName GetOwnerSid).Sid -ne $Identity.Value) { throw 'Owned launch identity could not be confirmed' }
    Write-Output ('Launched owned executable PID=' + $Started.Id + ' in existing interactive session=' + $Started.SessionId + '; same-user identity confirmed')
} finally {
    if ($Registered) {
        $Folder.DeleteTask($TaskName,0)
        try { $Folder.GetTask($TaskName) | Out-Null; throw 'Own temporary task remains registered' } catch { if ($_.Exception.HResult -eq -2147024894 -or $_.Exception.InnerException.HResult -eq -2147024894) { Write-Output 'PASS: exact own interactive QA task deleted' } else { throw } }
    }
}
