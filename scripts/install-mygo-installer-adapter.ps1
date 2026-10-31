param([string]$NsisDir = '')
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $NsisDir) {
    # Match MyGo's compiler search order: PATH, standard install, cache.
    $found = Get-Command makensis.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($found) { $NsisDir = Split-Path -Parent $found.Source }
    else {
        $candidates = @(
            (Join-Path ${env:ProgramFiles(x86)} 'NSIS'),
            (Join-Path $env:ProgramFiles 'NSIS'),
            (Join-Path $env:LOCALAPPDATA 'mygo\nsis-3.13')
        )
        $NsisDir = $candidates | Where-Object {
            (Test-Path -LiteralPath (Join-Path $_ 'makensis.exe')) -or
            (Test-Path -LiteralPath (Join-Path $_ 'Bin\makensis.exe'))
        } | Select-Object -First 1
    }
}
if (-not $NsisDir) { throw 'MyGo NSIS is not installed. Run a MyGo Windows build once, then run this script.' }
$bin = Join-Path $NsisDir 'Bin'
if (-not (Test-Path -LiteralPath $bin)) { $bin = $NsisDir }
$bin = (Resolve-Path -LiteralPath $bin).Path
$compiler = Join-Path $bin 'makensis.exe'
$real = Join-Path $bin 'makensis-real.exe'
if (-not (Test-Path -LiteralPath $compiler)) { throw "NSIS compiler not found: $compiler. Run a MyGo build once first." }
$running = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $compiler, [StringComparison]::OrdinalIgnoreCase) })
if ($running.Count -gt 0) { throw 'NSIS is currently running. Retry after the build finishes.' }
$temp = Join-Path ([IO.Path]::GetTempPath()) ('opendl-mygo-adapter-' + [guid]::NewGuid().ToString('N') + '.exe')
Push-Location $root
try {
    & go build -o $temp ./scripts/mygo-installer-adapter
    if ($LASTEXITCODE -ne 0) { throw 'Building the MyGo installer adapter failed.' }
    if (-not (Test-Path -LiteralPath $real)) { Copy-Item -LiteralPath $compiler -Destination $real }
    $version = & $real -VERSION
    if ($LASTEXITCODE -ne 0 -or "$version" -notmatch '(?i)\bv?\d+\.\d+') { throw "Original NSIS compiler could not be verified: $real" }
    $backup = Join-Path $bin ('makensis-before-opendl-' + [guid]::NewGuid().ToString('N') + '.exe')
    Copy-Item -LiteralPath $compiler -Destination $backup
    Copy-Item -LiteralPath $temp -Destination $compiler -Force
    Write-Host "Adapter: $compiler"
    Write-Host "Previous compiler backup: $backup"
    Write-Host "Other projects continue using: $real"
} finally {
    Pop-Location
    if (Test-Path -LiteralPath $temp) { Remove-Item -LiteralPath $temp -Force }
}
