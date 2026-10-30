# Removes the opendl native messaging host registration for Chrome and Edge.
#
#   .\scripts\uninstall-host.ps1 [-HostName com.example.opendl] [-RemoveFiles]
#
# -RemoveFiles deletes the shared host files only when no browser keeps a
# registration. User configuration (config directory) is never touched.

param(
  [string]$HostName = "com.example.opendl",
  [switch]$RemoveFiles
)

$ErrorActionPreference = "Stop"
$install = Join-Path $env:LOCALAPPDATA "opendl"

function Remove-Registration([string]$Key) {
  if (Test-Path $Key) {
    Remove-Item -Path $Key -Recurse -Force
    Write-Host "已移除注册: $Key"
  }
}

function Remove-SharedFiles {
  param([switch]$Enabled)
  if (-not $Enabled) { return }
  $leftover = @()
  foreach ($browser in @("Google\Chrome", "Microsoft\Edge", "Mozilla")) {
    $probe = "HKCU:\Software\$browser\NativeMessagingHosts\$HostName"
    if (Test-Path $probe) { $leftover += $probe }
  }
  if ($leftover.Count -gt 0) {
    Write-Host "仍有浏览器注册，保留共享文件: $($leftover -join ', ')"
    return
  }
  foreach ($file in @((Join-Path $install "$HostName.json"), (Join-Path $install "$HostName-firefox.json"), (Join-Path $install "idmhost.exe"))) {
    if (Test-Path $file) {
      Remove-Item -Path $file -Force
      Write-Host "已删除文件: $file"
    }
  }
}

foreach ($browser in @("Google\Chrome", "Microsoft\Edge")) {
  Remove-Registration "HKCU:\Software\$browser\NativeMessagingHosts\$HostName"
}
Remove-SharedFiles -Enabled:$RemoveFiles

Write-Host "已注销原生消息主机: $HostName"
Write-Host "请在 chrome://extensions 重新加载或移除扩展。"
