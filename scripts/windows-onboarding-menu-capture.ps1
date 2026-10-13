param([Parameter(Mandatory=$true)][int]$TrayProcessId,[Parameter(Mandatory=$true)][string]$OutputPath)
$ErrorActionPreference = 'Stop'
$RepositoryRoot = Split-Path -Parent $PSScriptRoot
$OutputPath = [IO.Path]::GetFullPath($OutputPath)
$AllowedRoot = [IO.Path]::GetFullPath((Join-Path $RepositoryRoot 'docs/qa/'))
if (-not $OutputPath.StartsWith($AllowedRoot,[StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetExtension($OutputPath) -ne '.png') { throw 'Screenshot output must be a PNG under the versioned QA directory' }
$Identity = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$Process = Get-CimInstance Win32_Process -Filter ('ProcessId=' + $TrayProcessId)
$Expected = Join-Path $env:LOCALAPPDATA 'DUMB/Worker/dumb-worker.exe'
if (-not $Process -or $Process.SessionId -le 0 -or $Process.ExecutablePath -ne $Expected -or $Process.CommandLine -notmatch 'internal-tray' -or (Invoke-CimMethod -InputObject $Process -MethodName GetOwnerSid).Sid -ne $Identity) { throw 'Exact same-user installed native tray process is required' }
Add-Type -AssemblyName System.Drawing
Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
public static class DUMBMenuCapture {
 [StructLayout(LayoutKind.Sequential)] public struct Rect {public int Left,Top,Right,Bottom;}
 public delegate bool Callback(IntPtr window,IntPtr data);
 [DllImport("user32.dll")] static extern bool EnumWindows(Callback callback,IntPtr data);
 [DllImport("user32.dll")] static extern bool IsWindowVisible(IntPtr window);
 [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr window,out uint process);
 [DllImport("user32.dll",CharSet=CharSet.Unicode)] static extern int GetClassName(IntPtr window,StringBuilder name,int count);
 [DllImport("user32.dll",SetLastError=true)] public static extern bool GetWindowRect(IntPtr window,out Rect rectangle);
 public static IntPtr Find(uint process){
  List<IntPtr> windows=new List<IntPtr>();
  EnumWindows(delegate(IntPtr window,IntPtr data){uint owner;GetWindowThreadProcessId(window,out owner);StringBuilder name=new StringBuilder(256);GetClassName(window,name,name.Capacity);if(owner==process && IsWindowVisible(window) && name.ToString()=="#32768")windows.Add(window);return true;},IntPtr.Zero);
  if(windows.Count!=1)throw new InvalidOperationException("Exactly one visible popup menu owned by the exact tray process is required");
  return windows[0];
 }
}
'@
$Window = [IntPtr]::Zero
$Deadline = [DateTime]::UtcNow.AddSeconds(40)
while ($Window -eq [IntPtr]::Zero -and [DateTime]::UtcNow -lt $Deadline) {
    try { $Window = [DUMBMenuCapture]::Find($TrayProcessId) } catch { Start-Sleep -Milliseconds 100 }
}
if ($Window -eq [IntPtr]::Zero) { throw 'The exact owned tray popup menu was not visible within the bounded capture window' }
$Rect = New-Object DUMBMenuCapture+Rect
if (-not [DUMBMenuCapture]::GetWindowRect($Window,[ref]$Rect)) { throw 'Owned popup menu rectangle unavailable' }
$Width = $Rect.Right - $Rect.Left
$Height = $Rect.Bottom - $Rect.Top
if ($Width -le 0 -or $Height -le 0 -or $Width -gt 1024 -or $Height -gt 1024) { throw 'Owned popup menu rectangle invalid' }
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $OutputPath) | Out-Null
$Image = New-Object Drawing.Bitmap($Width,$Height)
try {
    $Graphics = [Drawing.Graphics]::FromImage($Image)
    try { $Graphics.CopyFromScreen($Rect.Left,$Rect.Top,0,0,$Image.Size,[Drawing.CopyPixelOperation]::SourceCopy) } finally { $Graphics.Dispose() }
    $Image.Save($OutputPath,[Drawing.Imaging.ImageFormat]::Png)
} finally { $Image.Dispose() }
Write-Output 'PASS: only the exact native tray popup menu captured; no unrelated taskbar panel included'
