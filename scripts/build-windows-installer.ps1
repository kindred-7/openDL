param(
    [ValidateSet('amd64','arm64')][string]$Architecture = 'amd64',
    [string]$InnoSetup = ''
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$config = Get-Content -LiteralPath (Join-Path $root 'mygo.json') -Raw | ConvertFrom-Json
$temp = Join-Path ([IO.Path]::GetTempPath()) ('opendl-installer-build-' + [guid]::NewGuid().ToString('N'))
$oldSkip = $env:OPENDL_SKIP_MYGO_INSTALLER
New-Item -ItemType Directory -Path $temp | Out-Null
Push-Location $root
try {
    # The isolated build avoids replacing a development directory in use.
    $env:OPENDL_SKIP_MYGO_INSTALLER = '1'
    & go tool mygo build -platform "windows/$Architecture" -o $temp
    if ($LASTEXITCODE -ne 0) { throw 'MyGo application build failed.' }
    $stage = Join-Path $temp "windows-$Architecture"
    $name = "OpenDL Setup $($config.version) $Architecture.exe"
    $packageArgs = @{ Stage=$stage; OutputFile=(Join-Path $stage $name); Version=$config.version; Architecture=$Architecture }
    if ($InnoSetup) { $packageArgs.InnoSetup = $InnoSetup }
    & (Join-Path $PSScriptRoot 'package-windows-installer.ps1') @packageArgs
    $outputDir = Join-Path $root (Join-Path $config.out "windows-$Architecture")
    New-Item -ItemType Directory -Force -Path $outputDir | Out-Null
    $output = Join-Path $outputDir $name
    Move-Item -LiteralPath (Join-Path $stage $name) -Destination $output -Force
    Write-Host "Installer: $output"
} finally {
    $env:OPENDL_SKIP_MYGO_INSTALLER = $oldSkip
    Pop-Location
    if (Test-Path -LiteralPath $temp) {
        $resolved = (Resolve-Path -LiteralPath $temp).Path
        $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
        if (-not $resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or
            -not [IO.Path]::GetFileName($resolved).StartsWith('opendl-installer-build-')) { throw "Unexpected temporary path: $resolved" }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
