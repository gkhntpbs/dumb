param([Parameter(Mandatory=$true)][int]$WorkerProcessId,[Parameter(Mandatory=$true)][string]$OutputPath)
$ErrorActionPreference = 'Stop'
$RepositoryRoot = Split-Path -Parent $PSScriptRoot
$OutputPath = [IO.Path]::GetFullPath($OutputPath)
$AllowedRoot = [IO.Path]::GetFullPath((Join-Path $RepositoryRoot 'docs/qa/'))
if (-not $OutputPath.StartsWith($AllowedRoot,[StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetExtension($OutputPath) -ne '.png') { throw 'Screenshot output must be a PNG under the versioned QA directory' }
$Identity = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$Process = Get-CimInstance Win32_Process -Filter ('ProcessId=' + $WorkerProcessId)
$Expected = [IO.Path]::GetFullPath((Join-Path $RepositoryRoot 'dist/windows/dumb-worker.exe'))
if (-not $Process -or $Process.SessionId -le 0 -or $Process.ExecutablePath -ne $Expected -or (Invoke-CimMethod -InputObject $Process -MethodName GetOwnerSid).Sid -ne $Identity) { throw 'Exact same-user interactive QA setup process is required' }
Add-Type -AssemblyName System.Drawing
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
public static class DUMBConsoleCapture {
 [StructLayout(LayoutKind.Sequential)]public struct Rect {public int Left,Top,Right,Bottom;}
 [StructLayout(LayoutKind.Sequential)]public struct Point {public int X,Y;}
 [DllImport("kernel32.dll",SetLastError=true)]public static extern bool AttachConsole(uint id);
 [DllImport("kernel32.dll")]public static extern bool FreeConsole();
 [DllImport("kernel32.dll")]public static extern IntPtr GetConsoleWindow();
 [DllImport("user32.dll",SetLastError=true)]public static extern bool GetClientRect(IntPtr window,out Rect rectangle);
 [DllImport("user32.dll",SetLastError=true)]public static extern bool ClientToScreen(IntPtr window,ref Point point);
}
'@
[DUMBConsoleCapture]::FreeConsole() | Out-Null
if (-not [DUMBConsoleCapture]::AttachConsole($WorkerProcessId)) { throw 'Could not attach to the exact own setup console' }
try {
    $Window = [DUMBConsoleCapture]::GetConsoleWindow()
    $Rect = New-Object DUMBConsoleCapture+Rect
    $Point = New-Object DUMBConsoleCapture+Point
    if ($Window -eq [IntPtr]::Zero -or -not [DUMBConsoleCapture]::GetClientRect($Window,[ref]$Rect) -or -not [DUMBConsoleCapture]::ClientToScreen($Window,[ref]$Point)) { throw 'Own console client rectangle unavailable' }
    $Width = $Rect.Right - $Rect.Left
    $Height = $Rect.Bottom - $Rect.Top
    if ($Width -le 0 -or $Height -le 0 -or $Width -gt 4096 -or $Height -gt 4096) { throw 'Own console client rectangle invalid' }
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $OutputPath) | Out-Null
    $Image = New-Object Drawing.Bitmap($Width,$Height)
    try {
        $Graphics = [Drawing.Graphics]::FromImage($Image)
        try { $Graphics.CopyFromScreen($Point.X,$Point.Y,0,0,$Image.Size,[Drawing.CopyPixelOperation]::SourceCopy) } finally { $Graphics.Dispose() }
        $Image.Save($OutputPath,[Drawing.Imaging.ImageFormat]::Png)
    } finally { $Image.Dispose() }
} finally { [DUMBConsoleCapture]::FreeConsole() | Out-Null }
Write-Output 'PASS: exact own setup console client rectangle captured; no unrelated desktop or title bar included'
