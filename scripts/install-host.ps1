# Registers the opendl native messaging host for Chrome and Edge.
#
#   .\scripts\install-host.ps1 -ExtId <扩展 ID>
#
# The extension ID shows on chrome://extensions after loading
# extension/ as an unpacked extension.

param(
  [Parameter(Mandatory = $true)][string]$ExtId,
  [string]$HostName = "com.example.opendl"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$install = Join-Path $env:LOCALAPPDATA "opendl"
New-Item -ItemType Directory -Force -Path $install | Out-Null

Push-Location $root
go build -o (Join-Path $install "opendl-host.exe") .\cmd\opendl-host
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "go build opendl-host failed" }
Pop-Location

$manifest = Join-Path $install "$HostName.json"
$manifestObj = [ordered]@{
  name           = $HostName
  description    = "OpenDL native messaging host"
  path           = (Join-Path $install "opendl-host.exe")
  type           = "stdio"
  allowed_origins = @("chrome-extension://$ExtId/")
}
$manifestObj | ConvertTo-Json | Set-Content -Encoding UTF8 $manifest

foreach ($browser in @("Google\Chrome", "Microsoft\Edge")) {
  $key = "HKCU:\Software\$browser\NativeMessagingHosts\$HostName"
  New-Item -Path $key -Force | Out-Null
  Set-ItemProperty -Path $key -Name "(default)" -Value $manifest
}

Write-Host "已注册原生消息主机: $HostName"
Write-Host "  可执行文件: $(Join-Path $install 'opendl-host.exe')"
Write-Host "  manifest:   $manifest"
Write-Host "请在 chrome://extensions 重新加载扩展使配置生效。"
