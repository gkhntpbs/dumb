param([Parameter(Mandatory=$true)][int]$WorkerProcessId,[Parameter(Mandatory=$true)][int]$SessionId,[Parameter(Mandatory=$true)][string]$ServerURL,[Parameter(Mandatory=$true)][string]$TokenFile,[Parameter(Mandatory=$true)][string]$Models,[switch]$CaptureOnly,[switch]$TrayOnly,[string]$ScreenshotPath='', [ValidateSet('All','Server','Token','Models','Choice','Close')][string]$InputStage='All',[string]$Value='', [ValidateSet('Typed','CtrlV','RightClick')][string]$PasteMode='Typed')
$ErrorActionPreference = 'Stop'
$Identity = [Security.Principal.WindowsIdentity]::GetCurrent().User
$Process = Get-CimInstance Win32_Process -Filter ('ProcessId=' + $WorkerProcessId)
if (-not $Process -or $Process.SessionId -ne $SessionId -or (Invoke-CimMethod -InputObject $Process -MethodName GetOwnerSid).Sid -ne $Identity.Value) { throw 'Exact same-user process in the existing selected session is required' }
$RepositoryRoot = Split-Path -Parent $PSScriptRoot
$Expected = [IO.Path]::GetFullPath((Join-Path $RepositoryRoot 'dist/windows/dumb-worker.exe'))
if ($TrayOnly) { $Expected = Join-Path $env:LOCALAPPDATA 'DUMB/Worker/dumb-worker.exe'; if ($Process.CommandLine -notmatch 'internal-tray') { throw 'Exact native tray role is required' } }
if ($Process.ExecutablePath -ne $Expected) { throw 'Only the exact isolated QA worker or installed tray executable is allowed' }
$URI = [Uri]$ServerURL
if ($URI.Scheme -ne 'http' -or $URI.Host -ne '127.0.0.1') { throw 'Only an explicit loopback fixture backend is allowed' }
$TokenFile = [IO.Path]::GetFullPath($TokenFile)
$RunRoot = Split-Path -Parent $TokenFile
$Nonce = [guid]::NewGuid().ToString('N')
$Receipt = Join-Path $RunRoot ('console-input-' + $Nonce + '.json')
$TaskName = 'DUMB-QA-Console-' + $Nonce
function Quote-Literal([string]$Text) { return "'" + $Text.Replace("'","''") + "'" }
$Helper = Quote-Literal (Join-Path $PSScriptRoot 'windows-onboarding-console-input.ps1')
$Command = '$ErrorActionPreference=''Stop''; $stage=''server''; try { '
$Command += '& ' + $Helper + ' -WorkerProcessId ' + $WorkerProcessId + ' -Stage Server -Value ' + (Quote-Literal $ServerURL) + ' | Out-Null; Start-Sleep -Milliseconds 300; $stage=''token''; '
$Command += '& ' + $Helper + ' -WorkerProcessId ' + $WorkerProcessId + ' -Stage Token -TokenFile ' + (Quote-Literal $TokenFile) + ' | Out-Null; Start-Sleep -Milliseconds 300; '
$Command += '& ' + $Helper + ' -WorkerProcessId ' + $WorkerProcessId + ' -Stage Models -Value ' + (Quote-Literal $Models) + ' | Out-Null; '
$Command += '[IO.File]::WriteAllText(' + (Quote-Literal $Receipt) + ',''{"status":"passed","stages":"server,hidden-token,models"}'') '
$Command += '} catch { [IO.File]::WriteAllText(' + (Quote-Literal $Receipt) + ',(''failed at '' + $stage + '': '' + $_.Exception.Message)); exit 1 }'
if ($CaptureOnly) {
    $Capture = Quote-Literal (Join-Path $PSScriptRoot 'windows-onboarding-console-capture.ps1')
    $Command = '$ErrorActionPreference=''Stop''; try { & ' + $Capture + ' -WorkerProcessId ' + $WorkerProcessId + ' -OutputPath ' + (Quote-Literal $ScreenshotPath) + ' | Out-Null; [IO.File]::WriteAllText(' + (Quote-Literal $Receipt) + ',''{"status":"capture-passed"}'') } catch { [IO.File]::WriteAllText(' + (Quote-Literal $Receipt) + ',(''failed at capture: '' + $_.Exception.Message)); exit 1 }'
}
if ($TrayOnly) {
    $Capture = Quote-Literal (Join-Path $PSScriptRoot 'windows-onboarding-menu-capture.ps1')
    $Command = '$ErrorActionPreference=''Stop''; try { & ' + $Capture + ' -TrayProcessId ' + $WorkerProcessId + ' -OutputPath ' + (Quote-Literal $ScreenshotPath) + ' | Out-Null; [IO.File]::WriteAllText(' + (Quote-Literal $Receipt) + ',''{"status":"tray-capture-passed"}'') } catch { [IO.File]::WriteAllText(' + (Quote-Literal $Receipt) + ',(''failed at capture: '' + $_.Exception.Message)); exit 1 }'
}
if ($InputStage -ne 'All') {
    $InputCommand = '& ' + $Helper + ' -WorkerProcessId ' + $WorkerProcessId + ' -Stage ' + $InputStage + ' -PasteMode ' + $PasteMode
    if ($InputStage -eq 'Token') { $InputCommand += ' -TokenFile ' + (Quote-Literal $TokenFile) } else { $InputCommand += ' -Value ' + (Quote-Literal $Value) }
    $Command = '$ErrorActionPreference=''Stop''; try { ' + $InputCommand + ' | Out-Null; [IO.File]::WriteAllText(' + (Quote-Literal $Receipt) + ',''{"status":"single-input-passed"}'') } catch { [IO.File]::WriteAllText(' + (Quote-Literal $Receipt) + ',(''failed at input: '' + $_.Exception.Message)); exit 1 }'
}
$Encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($Command))
$Scheduler = New-Object -ComObject Schedule.Service
$Scheduler.Connect()
$Folder = $Scheduler.GetFolder('\')
$Definition = $Scheduler.NewTask(0)
$Definition.RegistrationInfo.Description = 'Temporary authorized exact-console DUMB QA fixture input'
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
$Registered = $false
try {
    $Task = $Folder.RegisterTaskDefinition($TaskName,$Definition,2,$Identity.Value,$null,3,$null)
    $Registered = $true
    $Task.Run($null) | Out-Null
    $Deadline = [DateTime]::UtcNow.AddSeconds(50)
    while (-not (Test-Path -LiteralPath $Receipt) -and [DateTime]::UtcNow -lt $Deadline) { Start-Sleep -Milliseconds 100 }
    if (-not (Test-Path -LiteralPath $Receipt)) { throw 'Exact-console input did not produce a bounded receipt' }
    $Result = [IO.File]::ReadAllText($Receipt)
    if ($Result.StartsWith('failed at')) { throw $Result }
    if ($InputStage -ne 'All') { Write-Output ('PASS: single ' + $InputStage + ' field submitted by native ' + $PasteMode + '; no secret contents printed') } elseif ($CaptureOnly -or $TrayOnly) { Write-Output 'PASS: exact same-user setup console client rectangle captured' } else { Write-Output 'PASS: exact same-user setup received Unicode fixture URL, hidden fixture token and configured models; no token contents printed' }
} finally {
    if ($Registered) {
        $Folder.DeleteTask($TaskName,0)
        try { $Folder.GetTask($TaskName) | Out-Null; throw 'Own console QA task remains registered' } catch { if ($_.Exception.HResult -eq -2147024894 -or $_.Exception.InnerException.HResult -eq -2147024894) { Write-Output 'PASS: exact own console QA task deleted' } else { throw } }
    }
}
