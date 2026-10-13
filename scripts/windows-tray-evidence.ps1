# Interactive-only, finite acceptance fixture. Captures only notification area/menu.
param(
    [Parameter(Mandatory=$true)][string]$Preview,
    [Parameter(Mandatory=$true)][string]$Helper,
    [Parameter(Mandatory=$true)][string]$Output,
    [int]$DurationSeconds = 120,
    [switch]$OpenMenu
)
$ErrorActionPreference = "Stop"
if (-not [Environment]::UserInteractive) { throw "An interactive desktop is required" }
if ($DurationSeconds -lt 20 -or $DurationSeconds -gt 300) { throw "Duration must be 20 through 300 seconds" }
Add-Type -AssemblyName System.Drawing
Add-Type @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
public static class TrayEvidence {
    public delegate bool EnumCallback(IntPtr hwnd, IntPtr arg);
    [StructLayout(LayoutKind.Sequential)] public struct Rect { public int Left,Top,Right,Bottom; }
    [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] public static extern bool EnumWindows(EnumCallback callback, IntPtr arg);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hwnd);
    [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hwnd, out uint pid);
    [DllImport("user32.dll",CharSet=CharSet.Unicode)] public static extern IntPtr FindWindow(string cls,string title);
    [DllImport("user32.dll",CharSet=CharSet.Unicode)] public static extern IntPtr FindWindowEx(IntPtr parent,IntPtr after,string cls,string title);
    [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hwnd,out Rect rect);
    [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr hwnd,uint msg,UIntPtr w,IntPtr l);
    public static int VisibleCount(uint[] ids) {
        int count=0;
        EnumWindows((hwnd,arg)=> {uint pid;GetWindowThreadProcessId(hwnd,out pid);if(Array.IndexOf(ids,pid)>=0 && IsWindowVisible(hwnd))count++;return true;},IntPtr.Zero);
        return count;
    }
}
'@
function Save-WindowCrop([IntPtr]$Handle, [string]$Destination) {
    $rect = New-Object TrayEvidence+Rect
    if ($Handle -eq [IntPtr]::Zero -or -not [TrayEvidence]::GetWindowRect($Handle, [ref]$rect)) { return $false }
    $width = $rect.Right - $rect.Left; $height = $rect.Bottom - $rect.Top
    if ($width -le 0 -or $height -le 0 -or $width -gt 1600 -or $height -gt 700) { return $false }
    $bitmap = New-Object System.Drawing.Bitmap($width, $height)
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    try {
        $graphics.CopyFromScreen($rect.Left, $rect.Top, 0, 0, $bitmap.Size)
        $bitmap.Save($Destination, [System.Drawing.Imaging.ImageFormat]::Png)
    } finally { $graphics.Dispose(); $bitmap.Dispose() }
    return $true
}
New-Item -ItemType Directory -Force -Path $Output | Out-Null
$before = [TrayEvidence]::GetForegroundWindow().ToInt64()
$arguments = @('-helper', ('"' + $Helper + '"'), '-duration', ($DurationSeconds.ToString() + 's'), '-log', ('"' + (Join-Path $Output 'preview.log') + '"'))
$process = Start-Process -FilePath $Preview -ArgumentList $arguments -PassThru
try {
    Start-Sleep -Seconds 4
    $after = [TrayEvidence]::GetForegroundWindow().ToInt64()
    $children = @(Get-CimInstance Win32_Process -Filter ("ParentProcessId=" + $process.Id))
    $ids = [uint32[]](@([uint32]$process.Id) + @($children | ForEach-Object { [uint32]$_.ProcessId }))
    $visible = [TrayEvidence]::VisibleCount($ids)
    $tray = [TrayEvidence]::FindWindow('Shell_TrayWnd', $null)
    $notify = [TrayEvidence]::FindWindowEx($tray,[IntPtr]::Zero,'TrayNotifyWnd',$null)
    $trayCaptured = Save-WindowCrop $notify (Join-Path $Output 'notification-area.png')
    $menuCaptured = $false
    if ($OpenMenu) {
        $window = [TrayEvidence]::FindWindow('DUMBTrayWindow',$null)
        [uint32]$ownerPid = 0
        [void][TrayEvidence]::GetWindowThreadProcessId($window,[ref]$ownerPid)
        if ($window -ne [IntPtr]::Zero -and $ids -contains $ownerPid) {
            # Explicit QA interaction, after measuring startup foreground state.
            [void][TrayEvidence]::PostMessage($window,0x8001,[UIntPtr]::new(1),[IntPtr]::new(0x205))
            Start-Sleep -Seconds 1
            $menu = [TrayEvidence]::FindWindow('#32768',$null)
            [uint32]$menuPid = 0
            [void][TrayEvidence]::GetWindowThreadProcessId($menu,[ref]$menuPid)
            if ($ids -contains $menuPid) {
                $menuCaptured = Save-WindowCrop $menu (Join-Path $Output 'tray-menu.png')
                [void][TrayEvidence]::PostMessage($menu,0x100,[UIntPtr]::new(0x1b),[IntPtr]::Zero)
            }
        }
    }
    [ordered]@{
        fixture = 'inert Windows tray preview'
        startupForegroundUnchanged = ($before -eq $after)
        startupVisibleOwnedWindowCount = $visible
        nativeHelperCount = $children.Count
        notificationAreaCaptured = $trayCaptured
        menuCaptured = $menuCaptured
        durationSeconds = $DurationSeconds
    } | ConvertTo-Json | Set-Content -Encoding UTF8 (Join-Path $Output 'metrics.json')
    $process.WaitForExit(($DurationSeconds + 10) * 1000) | Out-Null
} finally {
    # Only stop the exact process created by this finite fixture if it overstays.
    if (-not $process.HasExited) { $process.Kill(); $process.WaitForExit() }
}
