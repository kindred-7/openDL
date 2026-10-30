# Registers the opendl native messaging host for Firefox.
#
#   .\scripts\install-host-firefox.ps1 [-ExtId <扩展 ID>]
#
# The extension ID is the gecko id declared in extension/manifest-firefox.json
# (opendl@opendl.local). Load extension/manifest-firefox.json as a temporary
# add-on first, or sign the extension to keep it installed.

param(
  [Parameter(Mandatory = $false)][string]$ExtId = "opendl@opendl.local",
  [string]$HostName = "com.example.opendl"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$install = Join-Path $env:LOCALAPPDATA "opendl"
New-Item -ItemType Directory -Force -Path $install | Out-Null

$exe = Join-Path $install "idmhost.exe"
if (-not (Test-Path $exe)) {
  Push-Location $root
  go build -o $exe .\cmd\idmhost
  if ($LASTEXITCODE -ne 0) { Pop-Location; throw "go build idmhost failed" }
  Pop-Location
}

# Firefox reads allowed_extensions; the Chrome/Edge manifest keeps
# allowed_origins, so the two browsers get separate manifest files.
$manifest = Join-Path $install "$HostName-firefox.json"
$manifestObj = [ordered]@{
  name              = $HostName
  description       = "OpenDL native messaging host"
  path              = $exe
  type              = "stdio"
  allowed_extensions = @($ExtId)
}
$manifestObj | ConvertTo-Json | Set-Content -Encoding UTF8 $manifest

$key = "HKCU:\Software\Mozilla\NativeMessagingHosts\$HostName"
New-Item -Path $key -Force | Out-Null
Set-ItemProperty -Path $key -Name "(default)" -Value $manifest

Write-Host "已为 Firefox 注册原生消息主机: $HostName"
Write-Host "  可执行文件: $exe"
Write-Host "  manifest:   $manifest"
Write-Host "请在 about:debugging 重新加载扩展使配置生效。"
