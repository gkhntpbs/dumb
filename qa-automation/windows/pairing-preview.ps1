param([Parameter(Mandatory=$true)][string]$Executable,[Parameter(Mandatory=$true)][string]$OutputRoot)
$ErrorActionPreference='Stop'
$Executable=[IO.Path]::GetFullPath($Executable)
$OutputRoot=[IO.Path]::GetFullPath($OutputRoot)
if(Test-Path $OutputRoot){throw 'Preview destination must be new'}
New-Item -ItemType Directory $OutputRoot | Out-Null
Add-Type -AssemblyName System.Drawing
Add-Type @'
using System;using System.Text;using System.Runtime.InteropServices;
public static class PairingPreview {
[StructLayout(LayoutKind.Sequential)]public struct Coord{public short X,Y;}
[StructLayout(LayoutKind.Sequential)]public struct Rect{public int L,T,R,B;}
[StructLayout(LayoutKind.Sequential)]public struct SmallRect{public short L,T,R,B;}
[StructLayout(LayoutKind.Sequential)]public struct Info{public Coord Size,Cursor;public ushort Attributes;public SmallRect Window;public Coord Max;}
[StructLayout(LayoutKind.Explicit,Size=20,CharSet=CharSet.Unicode)]public struct Input{[FieldOffset(0)]public ushort Type;[FieldOffset(4)]public int Down;[FieldOffset(8)]public ushort Repeat;[FieldOffset(10)]public ushort Virtual;[FieldOffset(14)]public char Character;}
[DllImport("kernel32.dll")]public static extern bool FreeConsole();
[DllImport("kernel32.dll",SetLastError=true)]public static extern bool AttachConsole(uint p);
[DllImport("kernel32.dll")]public static extern IntPtr GetConsoleWindow();
[DllImport("kernel32.dll",CharSet=CharSet.Unicode)]public static extern IntPtr CreateFile(string p,uint a,uint s,IntPtr x,uint d,uint f,IntPtr t);
[DllImport("kernel32.dll")]public static extern bool CloseHandle(IntPtr h);
[DllImport("kernel32.dll")]public static extern bool GetConsoleScreenBufferInfo(IntPtr h,out Info i);
[DllImport("kernel32.dll",CharSet=CharSet.Unicode)]public static extern bool ReadConsoleOutputCharacter(IntPtr h,StringBuilder b,uint n,Coord c,out uint r);
[DllImport("kernel32.dll")]public static extern bool WriteConsoleInput(IntPtr h,Input[] a,uint n,out uint w);
[DllImport("user32.dll")]public static extern bool GetWindowRect(IntPtr h,out Rect r);
public static string Read(){var h=CreateFile("CONOUT$",0x80000000,3,IntPtr.Zero,3,0,IntPtr.Zero);try{Info i;if(!GetConsoleScreenBufferInfo(h,out i))throw new Exception("Console unavailable");var b=new StringBuilder(10000);var all=new StringBuilder();for(short y=0;y<=i.Cursor.Y;y++){uint r;var c=new Coord();c.Y=y;b.Clear();ReadConsoleOutputCharacter(h,b,(uint)i.Size.X,c,out r);all.AppendLine(b.ToString().TrimEnd());}return all.ToString();}finally{CloseHandle(h);}}
public static void Enter(){var h=CreateFile("CONIN$",0xC0000000,3,IntPtr.Zero,3,0,IntPtr.Zero);try{var a=new Input[2];a[0].Type=1;a[0].Down=1;a[0].Repeat=1;a[0].Virtual=13;a[0].Character='\r';a[1]=a[0];a[1].Down=0;uint w;if(!WriteConsoleInput(h,a,2,out w)||w!=2)throw new Exception("Console input failed");}finally{CloseHandle(h);}}
}
'@
$oldLocal=$env:LOCALAPPDATA
$oldServer=$env:DUMB_SERVER
$child=$null
try {
 $env:LOCALAPPDATA=Join-Path $OutputRoot 'isolated-localappdata'
 $env:DUMB_SERVER='https://127.0.0.1:1'
 $child=Start-Process -FilePath $Executable -PassThru
 Start-Sleep -Seconds 2
 if($child.HasExited){throw 'Preview exited'}
 [void][PairingPreview]::FreeConsole()
 if(-not [PairingPreview]::AttachConsole($child.Id)){throw 'Cannot attach exact preview console'}
 $text=[PairingPreview]::Read()
 if(-not $text.Contains('Server URL [https://127.0.0.1:1]')){throw 'Unexpected initial preview prompt'}
 [PairingPreview]::Enter()
 Start-Sleep -Milliseconds 500
 $text=[PairingPreview]::Read()
 if(-not $text.Contains('Pairing code or worker token [P/T] (default P)')){throw 'Pairing selection not shown'}
 [IO.File]::WriteAllText((Join-Path $OutputRoot 'auth-choice.txt'),$text)
 $rect=New-Object PairingPreview+Rect
 if(-not [PairingPreview]::GetWindowRect([PairingPreview]::GetConsoleWindow(),[ref]$rect)){throw 'Preview window unavailable'}
 $width=$rect.R-$rect.L;$height=$rect.B-$rect.T
 if($width -lt 1 -or $height -lt 1 -or $width -gt 2000 -or $height -gt 1500){throw 'Preview crop invalid'}
 $bitmap=New-Object Drawing.Bitmap($width,$height)
 $graphics=[Drawing.Graphics]::FromImage($bitmap)
 try{$graphics.CopyFromScreen($rect.L,$rect.T,0,0,$bitmap.Size);$bitmap.Save((Join-Path $OutputRoot 'auth-choice.png'),[Drawing.Imaging.ImageFormat]::Png)}finally{$graphics.Dispose();$bitmap.Dispose()}
 [IO.File]::WriteAllText((Join-Path $OutputRoot 'result.txt'),'PASS: native pairing selection captured; no credential submitted')
} catch {
 [IO.File]::WriteAllText((Join-Path $OutputRoot 'error.txt'),$_.Exception.Message)
 throw
} finally {
 [void][PairingPreview]::FreeConsole()
 if($child -and -not $child.HasExited){Stop-Process -Id $child.Id}
 $env:LOCALAPPDATA=$oldLocal
 $env:DUMB_SERVER=$oldServer
}
