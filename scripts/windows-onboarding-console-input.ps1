param([Parameter(Mandatory=$true)][int]$WorkerProcessId,[Parameter(Mandatory=$true)][ValidateSet('Server','Token','Models','Choice','Close')][string]$Stage,[string]$TokenFile='',[string]$Value='', [ValidateSet('Typed','CtrlV','RightClick')][string]$PasteMode='Typed')
$ErrorActionPreference = 'Stop'
if ($Stage -eq 'Token') { $TokenFile = [IO.Path]::GetFullPath($TokenFile); if (-not (Test-Path -LiteralPath $TokenFile -PathType Leaf)) { throw 'Fixture token file is missing' } }
$Identity = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$Process = Get-CimInstance Win32_Process -Filter ('ProcessId=' + $WorkerProcessId)
if (-not $Process -or $Process.Name -ne 'dumb-worker.exe' -or $Process.SessionId -le 0 -or (Invoke-CimMethod -InputObject $Process -MethodName GetOwnerSid).Sid -ne $Identity) { throw 'Exact same-user interactive worker process is required' }
$RepositoryRoot = Split-Path -Parent $PSScriptRoot
$Expected = [IO.Path]::GetFullPath((Join-Path $RepositoryRoot 'dist/windows/dumb-worker.exe'))
if ($Process.ExecutablePath -ne $Expected) { throw 'Only the isolated QA worker executable may receive fixture input' }
$DevelopmentRoot = Split-Path -Parent $RepositoryRoot
if ($Stage -eq 'Token' -and (-not $TokenFile.StartsWith((Join-Path $DevelopmentRoot 'evidence/onboarding-server-'),[StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetFileName($TokenFile) -ne 'worker.token')) { throw 'Only an isolated onboarding fixture token file is allowed' }
if (-not ('DUMBConsoleToken' -as [type])) {
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Text;
public static class DUMBConsoleToken {
 [StructLayout(LayoutKind.Sequential)] struct Coord {public short X,Y;}
 [StructLayout(LayoutKind.Sequential)] struct Rect {public short Left,Top,Right,Bottom;}
 [StructLayout(LayoutKind.Sequential)] struct BufferInfo {public Coord Size,Cursor;public ushort Attributes;public Rect Window;public Coord Maximum;}
 [StructLayout(LayoutKind.Explicit,Size=20,CharSet=CharSet.Unicode)] struct Input {
  [FieldOffset(0)]public ushort Type;[FieldOffset(4)]public int Down;[FieldOffset(8)]public ushort Repeat;[FieldOffset(10)]public ushort Virtual;[FieldOffset(12)]public ushort Scan;[FieldOffset(14)]public char Character;[FieldOffset(16)]public uint State;
 }
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool AttachConsole(uint id);
 [DllImport("kernel32.dll")] static extern bool FreeConsole();
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr CreateFile(string path,uint access,uint sharing,IntPtr attributes,uint disposition,uint flags,IntPtr template);
 [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool GetConsoleMode(IntPtr handle,out uint mode);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool GetConsoleScreenBufferInfo(IntPtr handle,out BufferInfo info);
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern bool ReadConsoleOutputCharacter(IntPtr handle,IntPtr text,uint length,Coord point,out uint read);
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern bool WriteConsoleInput(IntPtr handle,Input[] input,uint length,out uint written);
 [DllImport("kernel32.dll")] static extern IntPtr GetConsoleWindow();
 [DllImport("user32.dll",SetLastError=true)] static extern bool OpenClipboard(IntPtr window);
 [DllImport("user32.dll")] static extern bool CloseClipboard();
 [DllImport("user32.dll",SetLastError=true)] static extern bool EmptyClipboard();
 [DllImport("user32.dll",SetLastError=true)] static extern IntPtr GetClipboardData(uint format);
 [DllImport("user32.dll")] static extern bool IsClipboardFormatAvailable(uint format);
 [DllImport("user32.dll")] static extern uint GetClipboardSequenceNumber();
 [DllImport("user32.dll",SetLastError=true)] static extern IntPtr SetClipboardData(uint format,IntPtr memory);
 [DllImport("kernel32.dll",SetLastError=true)] static extern IntPtr GlobalAlloc(uint flags,UIntPtr bytes);
 [DllImport("kernel32.dll",SetLastError=true)] static extern IntPtr GlobalLock(IntPtr memory);
 [DllImport("kernel32.dll")] static extern bool GlobalUnlock(IntPtr memory);
 [DllImport("kernel32.dll")] static extern IntPtr GlobalFree(IntPtr memory);
 [DllImport("user32.dll",SetLastError=true)] static extern bool PostMessage(IntPtr window,uint message,IntPtr w,IntPtr l);
 static void Clipboard(string text){
  byte[] bytes=Encoding.Unicode.GetBytes(text+"\0");
  IntPtr memory=GlobalAlloc(2,(UIntPtr)(uint)bytes.Length);
  if(memory==IntPtr.Zero)throw new Win32Exception(Marshal.GetLastWin32Error());
  bool transferred=false;
  try{
   IntPtr pointer=GlobalLock(memory);if(pointer==IntPtr.Zero)throw new Win32Exception(Marshal.GetLastWin32Error());
   try{Marshal.Copy(bytes,0,pointer,bytes.Length);}finally{GlobalUnlock(memory);}
   if(!OpenClipboard(GetConsoleWindow()))throw new Win32Exception(Marshal.GetLastWin32Error());
   try{if(!EmptyClipboard()||SetClipboardData(13,memory)==IntPtr.Zero)throw new Win32Exception(Marshal.GetLastWin32Error());transferred=true;}finally{CloseClipboard();}
  }finally{if(!transferred)GlobalFree(memory);}
  if(!IsClipboardFormatAvailable(13)||!OpenClipboard(GetConsoleWindow()))throw new InvalidOperationException("Independent Unicode clipboard availability check failed");
  try{
   IntPtr data=GetClipboardData(13);if(data==IntPtr.Zero)throw new Win32Exception(Marshal.GetLastWin32Error(),"Independent Unicode clipboard read failed");
   IntPtr pointer=GlobalLock(data);try{if(Marshal.PtrToStringUni(pointer)!=text)throw new InvalidOperationException("Independent Unicode clipboard value check failed");}finally{GlobalUnlock(data);}
  }finally{CloseClipboard();}
 }
 public static void Submit(uint process,string token,string expected,bool secret,string pasteMode){
  FreeConsole();if(!AttachConsole(process))throw new Win32Exception(Marshal.GetLastWin32Error(),"AttachConsole failed");
  try {
   IntPtr input=CreateFile("CONIN$",0xC0000000,3,IntPtr.Zero,3,0,IntPtr.Zero),output=CreateFile("CONOUT$",0xC0000000,3,IntPtr.Zero,3,0,IntPtr.Zero);uint mode;
   try {
   if(!GetConsoleMode(input,out mode))throw new Win32Exception(Marshal.GetLastWin32Error());
   if(secret && (mode&4)!=0)throw new InvalidOperationException("Exact console is not in hidden-token input mode");
   BufferInfo info;if(!GetConsoleScreenBufferInfo(output,out info))throw new Win32Exception(Marshal.GetLastWin32Error());
   string line;uint read;Coord point=new Coord();point.Y=info.Cursor.Y;
   IntPtr buffer=Marshal.AllocHGlobal((info.Cursor.X+1)*2);
   try{
    if(!ReadConsoleOutputCharacter(output,buffer,(uint)info.Cursor.X,point,out read))throw new Win32Exception(Marshal.GetLastWin32Error());
    line=Marshal.PtrToStringUni(buffer,(int)read);
   }finally{Marshal.FreeHGlobal(buffer);}
   if(line.Trim()!=expected)throw new InvalidOperationException("Exact console is not at the expected setup prompt; nativeRead="+read+" cursorX="+info.Cursor.X);
   if((secret&&String.IsNullOrWhiteSpace(token))||token.IndexOfAny(new char[]{'\r','\n','\0'})>=0)throw new InvalidOperationException("Fixture token format is invalid");
   string text=token+"\r";
   if(pasteMode!="Typed"){
    Clipboard(token);
    if(pasteMode=="RightClick"){
     IntPtr window=GetConsoleWindow();
     if(!PostMessage(window,0x204,(IntPtr)2,(IntPtr)0x000A000A)||!PostMessage(window,0x205,IntPtr.Zero,(IntPtr)0x000A000A))throw new Win32Exception(Marshal.GetLastWin32Error());
     System.Threading.Thread.Sleep(200);
     text="\r";
    }else{text="\u0016\r";}
   }
   Input[] events=new Input[text.Length*2];
   for(int i=0;i<text.Length;i++){
    Input item=new Input();item.Type=1;item.Down=1;item.Repeat=1;item.Character=text[i];if(text[i]=='\r')item.Virtual=13;if(text[i]=='\u0016'){item.Virtual=86;item.State=8;}
    events[i*2]=item;item.Down=0;events[i*2+1]=item;
   }
   uint written;if(!WriteConsoleInput(input,events,(uint)events.Length,out written)||written!=events.Length)throw new Win32Exception(Marshal.GetLastWin32Error(),"WriteConsoleInput failed");
  } finally {CloseHandle(input);CloseHandle(output);}
  } finally {FreeConsole();}
 }
}
'@
}
$ExpectedPrompt = switch ($Stage) {
    'Server' { 'Server URL:' }
    'Token' { 'Pre-issued worker token (hidden):' }
    'Models' { 'Ollama models (comma-separated):' }
    'Choice' { 'Retry or Exit [R/E]:' }
    'Close' { 'Press Enter to close this setup terminal:' }
}
if ($Stage -eq 'Token') { $Value = [IO.File]::ReadAllText($TokenFile).Trim() }
[DUMBConsoleToken]::Submit($WorkerProcessId,$Value,$ExpectedPrompt,($Stage -eq 'Token'),$PasteMode)
$Value = $null
Write-Output ('PASS: ' + $Stage + ' input submitted only to the exact same-user setup prompt; secret contents were not printed')
