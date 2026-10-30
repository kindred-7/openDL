param(
    [ValidateSet('amd64', 'arm64')][string]$Architecture = 'amd64',
    [string]$Makensis = ''
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $config = Get-Content -LiteralPath (Join-Path $root 'mygo.json') -Raw | ConvertFrom-Json
    & go tool mygo build -platform "windows/$Architecture"
    if ($LASTEXITCODE -ne 0) { throw 'MyGo build failed.' }
    $stage = Join-Path $root (Join-Path $config.out "windows-$Architecture")
    $oldInstaller = Join-Path $stage "OpenDL Setup $($config.version) $Architecture.exe"
    # Do not leave the stock installer in the release directory if this step fails.
    if (Test-Path -LiteralPath $oldInstaller) { Remove-Item -LiteralPath $oldInstaller -Force }
    if (-not $Makensis) {
        $found = Get-Command makensis.exe -ErrorAction SilentlyContinue
        if ($found) { $Makensis = $found.Source }
        else {
            $candidates = @(
                (Join-Path ${env:ProgramFiles(x86)} 'NSIS\makensis.exe'),
                (Join-Path $env:ProgramFiles 'NSIS\makensis.exe'),
                (Join-Path $env:LOCALAPPDATA 'mygo\nsis-3.13\Bin\makensis.exe'),
                (Join-Path $env:LOCALAPPDATA 'mygo\nsis-3.13\makensis.exe')
            )
            $Makensis = $candidates | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
        }
    }
    if (-not $Makensis) { throw 'NSIS makensis.exe not found. Install NSIS or pass -Makensis.' }
    $output = Join-Path $stage "OpenDL.Setup.$($config.version).$Architecture.exe"
    $temporary = Join-Path ([IO.Path]::GetTempPath()) ("opendl-setup-" + [Guid]::NewGuid().ToString('N') + '.exe')
    try {
        & $Makensis "/DSTAGE=$stage" "/DOUTPUT=$temporary" "/DVERSION=$($config.version)" "/DHELPER=$(Join-Path $PSScriptRoot 'installer-stop.ps1')" (Join-Path $PSScriptRoot 'windows-installer.nsi')
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $temporary)) { throw 'NSIS installer generation failed.' }
        Move-Item -LiteralPath $temporary -Destination $output -Force
    } finally {
        if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force }
    }
    Write-Host "Installer: $output"
} finally { Pop-Location }
