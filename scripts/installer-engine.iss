; Private, silent installation engine. The visible wizard is cmd/installer.
#ifndef VERSION
  #error VERSION is required
#endif
#ifndef ARCH
  #error ARCH is required
#endif
#ifndef ARCHITECTURES_ALLOWED
  #error ARCHITECTURES_ALLOWED is required
#endif
#ifndef STAGE
  #error STAGE is required
#endif
#ifndef ICON
  #error ICON is required
#endif
#ifndef HELPER
  #error HELPER is required
#endif

[Setup]
AppId=com.example.opendl
AppName=OpenDL
AppVersion={#VERSION}
DefaultDirName={localappdata}\Programs\OpenDL
PrivilegesRequired=lowest
DisableProgramGroupPage=yes
ArchitecturesAllowed={#ARCHITECTURES_ALLOWED}
OutputDir={#STAGE}
OutputBaseFilename=OpenDL.InstallEngine.{#VERSION}.{#ARCH}
SetupIconFile={#ICON}
UninstallDisplayIcon={app}\OpenDL.exe
Compression=lzma2/max
SolidCompression=yes
CloseApplications=yes
RestartApplications=no

[Tasks]
Name: "desktopicon"; Description: "Desktop shortcut"; Flags: unchecked
Name: "autostart"; Description: "Start at login"; Flags: unchecked

[Files]
Source: "{#STAGE}\OpenDL.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#STAGE}\icon.png"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#STAGE}\extension\*"; DestDir: "{app}\extension"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "{#HELPER}"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#HELPER}"; Flags: dontcopy

[Icons]
Name: "{autoprograms}\OpenDL"; Filename: "{app}\OpenDL.exe"
Name: "{autodesktop}\OpenDL"; Filename: "{app}\OpenDL.exe"; Tasks: desktopicon

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "com.example.opendl"; ValueData: """{app}\OpenDL.exe"" --mygo-opened-at-login"; Tasks: autostart; Flags: uninsdeletevalue

[UninstallRun]
Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoProfile -NonInteractive -ExecutionPolicy Bypass -File ""{app}\installer-stop.ps1"" -Executable ""{app}\OpenDL.exe"""; Flags: runhidden; RunOnceId: "StopOpenDL"

[Code]
var
  LastProgress: Integer;

function StatusFile(): String;
var
  I: Integer;
  Arg, Prefix: String;
begin
  Result := '';
  Prefix := '/OPENDLSTATUS=';
  for I := 1 to ParamCount do
  begin
    Arg := ParamStr(I);
    if CompareText(Copy(Arg, 1, Length(Prefix)), Prefix) = 0 then
    begin
      Result := Copy(Arg, Length(Prefix) + 1, Length(Arg));
      Exit;
    end;
  end;
end;

procedure Status(const Value: String);
var
  FileName: String;
begin
  FileName := StatusFile();
  if FileName <> '' then
    SaveStringToFile(FileName, Value + #13#10, True);
end;

procedure CurInstallProgressChanged(CurProgress, MaxProgress: Integer);
var
  Percent: Integer;
begin
  if MaxProgress > 0 then
  begin
    Percent := (Int64(CurProgress) * 100) div MaxProgress;
    if Percent > 99 then Percent := 99;
    if Percent <> LastProgress then
    begin
      LastProgress := Percent;
      Status(Format('P=%d', [Percent]));
    end;
  end;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  LegacyDir: String;
begin
  if CurStep = ssInstall then Status('S=INSTALL');
  if CurStep = ssPostInstall then
  begin
    Status('S=FINALIZE');
    if not WizardIsTaskSelected('autostart') then
      RegDeleteValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Run', 'com.example.opendl')
    else
      RegDeleteValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run', 'com.example.opendl');
  end;
  if (CurStep = ssPostInstall) and
    RegQueryStringValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl',
      'InstallLocation', LegacyDir) and
    (CompareText(AddBackslash(LegacyDir), AddBackslash(ExpandConstant('{app}'))) = 0) then
    RegDeleteKeyIncludingSubkeys(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl');
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
  Params: String;
begin
  Result := '';
  NeedsRestart := False;
  LastProgress := -1;
  Status('S=PREPARE');
  if not FileExists(ExpandConstant('{app}\OpenDL.exe')) then
    Exit;
  Status('S=CLOSE');
  ExtractTemporaryFile('installer-stop.ps1');
  Params := '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "' +
    ExpandConstant('{tmp}\installer-stop.ps1') + '" -Executable "' +
    ExpandConstant('{app}\OpenDL.exe') + '"';
  if not Exec(ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'), Params,
    '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
  begin
    Result := '无法启动退出辅助程序。';
    Log(Result);
    Exit;
  end;
  if ResultCode = 4 then
  begin
    Status('S=FORCE');
    Params := Params + ' -ForceLegacy';
    if not Exec(ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'), Params,
      '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
    begin
      Result := '无法启动退出辅助程序。';
      Log(Result);
      Exit;
    end;
  end;
  if ResultCode <> 0 then
  begin
    Result := Format('无法关闭 OpenDL，退出检查代码：%d', [ResultCode]);
    Log(Result);
  end;
  if ResultCode = 0 then Status('S=CLOSED');
end;
