Unicode true
ManifestDPIAware true
SetCompressor /SOLID lzma
!include "MUI2.nsh"
!include "LogicLib.nsh"
Name "OpenDL"
OutFile "${OUTPUT}"
InstallDir "$LOCALAPPDATA\Programs\OpenDL"
InstallDirRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl" "InstallLocation"
RequestExecutionLevel user
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\OpenDL.exe"
!define MUI_FINISHPAGE_SHOWREADME
!define MUI_FINISHPAGE_SHOWREADME_TEXT "创建桌面快捷方式"
!define MUI_FINISHPAGE_SHOWREADME_FUNCTION CreateDesktopShortcut
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

Section
  InitPluginsDir
  SetOutPath "$PLUGINSDIR"
  File /oname=installer-stop.ps1 "${HELPER}"
  IfFileExists "$INSTDIR\OpenDL.exe" 0 ready
  IfSilent try_close 0
  MessageBox MB_OKCANCEL|MB_ICONINFORMATION "安装将关闭此目录中运行的 OpenDL，并暂停下载任务。如果旧版本无法正常退出，安装器将在等待后强制结束该目录中的进程，可能丢失未保存的状态或中断下载。请先保存工作。" IDOK try_close
  Abort
  try_close:
    DetailPrint "正在等待旧版 OpenDL 退出并释放文件..."
    nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$PLUGINSDIR\installer-stop.ps1" -Executable "$INSTDIR\OpenDL.exe"'
    Pop $0
    Pop $1
    StrCmp $0 0 ready
    IfSilent failed 0
    StrCmp $0 4 legacy_running
  retry_locked:
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "无法关闭或写入 $INSTDIR\OpenDL.exe。请退出该位置的 OpenDL，并检查其他程序是否占用文件或安装目录是否可写，然后重试。$\r$\n退出检查结果：$0$\r$\n$1" IDRETRY try_close
    Goto failed
  legacy_running:
    DetailPrint "旧版未能正常退出，正在结束目标目录中的进程..."
    Goto force_legacy
  force_legacy:
    nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$PLUGINSDIR\installer-stop.ps1" -Executable "$INSTDIR\OpenDL.exe" -ForceLegacy'
    Pop $0
    Pop $1
    StrCmp $0 0 ready retry_locked
  failed:
    SetErrorLevel 1
    Abort
  ready:
  SetOutPath "$INSTDIR"
  SetOverwrite on
  ; Preserve the previous executable until every packaged file was copied.
  StrCpy $2 0
  IfFileExists "$INSTDIR\OpenDL.exe" 0 extract
  ClearErrors
  GetTempFileName $3
  IfErrors backup_failed
  CopyFiles /SILENT "$INSTDIR\OpenDL.exe" "$3"
  IfErrors backup_failed
  StrCpy $2 1
  extract:
  ClearErrors
  File "${STAGE}\OpenDL.exe"
  IfErrors extract_failed
  ClearErrors
  File "${STAGE}\icon.png"
  IfErrors extract_failed
  ClearErrors
  File /r "${STAGE}\extension"
  IfErrors extract_failed
  SetOutPath "$INSTDIR"
  ClearErrors
  File /oname=installer-stop.ps1 "${HELPER}"
  IfErrors extract_failed
  ClearErrors
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  IfErrors extract_failed
  CreateShortCut "$SMPROGRAMS\OpenDL.lnk" "$INSTDIR\OpenDL.exe"
  ClearErrors
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl" "DisplayName" "OpenDL"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl" "DisplayIcon" "$INSTDIR\OpenDL.exe"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl" "NoModify" 1
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl" "NoRepair" 1
  IfErrors extract_failed
  Goto installed
  backup_failed:
    SetErrorLevel 1
    IfSilent 0 +2
    Abort
    MessageBox MB_OK|MB_ICONSTOP "无法备份当前程序。安装已停止，原程序未被替换。"
    Abort
  extract_failed:
    StrCmp $2 1 0 extract_failed_notice
    ClearErrors
    CopyFiles /SILENT "$3" "$INSTDIR\OpenDL.exe"
    IfErrors restore_failed
    Delete "$3"
  extract_failed_notice:
    SetErrorLevel 1
    IfSilent 0 +2
    Abort
    MessageBox MB_OK|MB_ICONSTOP "安装文件或安装登记写入失败。若存在旧版，已尝试恢复其程序文件；扩展文件和安装登记可能需要重新修复。"
    Abort
  restore_failed:
    SetErrorLevel 1
    IfSilent 0 +2
    Abort
    MessageBox MB_OK|MB_ICONSTOP "安装失败且无法恢复旧程序。旧程序的备份保留在：$3。请检查安装目录权限和文件占用后手动恢复。"
    Abort
  installed:
    StrCmp $2 1 0 +2
    Delete "$3"
SectionEnd

Function CreateDesktopShortcut
  CreateShortCut "$DESKTOP\OpenDL.lnk" "$INSTDIR\OpenDL.exe"
FunctionEnd

Section "Uninstall"
  IfFileExists "$INSTDIR\OpenDL.exe" 0 un_remove
  IfFileExists "$INSTDIR\installer-stop.ps1" 0 un_delete
  IfSilent un_wait 0
  MessageBox MB_OKCANCEL|MB_ICONINFORMATION "卸载将关闭此目录中运行的 OpenDL，并暂停下载任务。请先保存工作。" IDOK un_wait
  Abort
  un_wait:
    nsExec::ExecToStack '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$INSTDIR\installer-stop.ps1" -Executable "$INSTDIR\OpenDL.exe"'
    Pop $0
    Pop $1
    StrCmp $0 0 un_delete
    IfSilent un_failed 0
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "无法关闭或访问 OpenDL。请先退出程序并检查文件占用，然后重试。" IDRETRY un_wait
    Goto un_failed
  un_delete:
    ClearErrors
    Delete "$INSTDIR\OpenDL.exe"
    IfErrors un_failed
  un_remove:
  Delete "$SMPROGRAMS\OpenDL.lnk"
  Delete "$DESKTOP\OpenDL.lnk"
  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.opendl"
  Delete "$INSTDIR\icon.png"
  Delete "$INSTDIR\installer-stop.ps1"
  RMDir /r "$INSTDIR\extension"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
  Goto un_done
  un_failed:
    SetErrorLevel 1
    IfSilent 0 +2
    Abort
    MessageBox MB_OK|MB_ICONSTOP "无法删除 OpenDL.exe，卸载已停止，卸载入口仍然保留。"
    Abort
  un_done:
SectionEnd
