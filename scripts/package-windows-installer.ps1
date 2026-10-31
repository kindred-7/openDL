param(
    [Parameter(Mandatory=$true)][string]$Stage,
    [Parameter(Mandatory=$true)][string]$OutputFile,
    [Parameter(Mandatory=$true)][string]$Version,
    [Parameter(Mandatory=$true)][ValidateSet('amd64','arm64')][string]$Architecture,
    [string]$InnoSetup = ''
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$stagePath = (Resolve-Path -LiteralPath $Stage).Path
$outputPath = [IO.Path]::GetFullPath($OutputFile)
if (-not [string]::Equals([IO.Path]::GetDirectoryName($outputPath), $stagePath, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Installer output must be inside its staging directory.'
}
if (-not $InnoSetup) {
    $found = Get-Command iscc.exe -ErrorAction SilentlyContinue
    if ($found) { $InnoSetup = $found.Source }
    else {
        $candidates = @(
            (Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6\ISCC.exe'),
            (Join-Path $env:ProgramFiles 'Inno Setup 6\ISCC.exe'),
            (Join-Path $env:LOCALAPPDATA 'Programs\Inno Setup 6\ISCC.exe')
        )
        $InnoSetup = $candidates | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
    }
}
if (-not $InnoSetup) { throw 'Inno Setup 6.6 or newer not found. Install it or pass -InnoSetup.' }
$temp = Join-Path ([IO.Path]::GetTempPath()) ('opendl-package-' + [guid]::NewGuid().ToString('N'))
$configDir = Join-Path $root ('.installer-build-config-' + [guid]::NewGuid().ToString('N'))
$engine = Join-Path $stagePath "OpenDL.InstallEngine.$Version.$Architecture.exe"
$oldSkip = $env:OPENDL_SKIP_MYGO_INSTALLER
New-Item -ItemType Directory -Path $temp | Out-Null
Push-Location $root
try {
    $icon = Join-Path $temp 'icon.ico'
    $packaged = Join-Path $temp 'setup.exe'
    & (Join-Path $PSScriptRoot 'create-installer-icon.ps1') -Source (Join-Path $root 'resources/icon.png') -Output $icon
    $allowed = if ($Architecture -eq 'arm64') { 'arm64' } else { 'x64compatible' }
    & $InnoSetup '/Q' "/DVERSION=$Version" "/DARCH=$Architecture" "/DARCHITECTURES_ALLOWED=$allowed" "/DSTAGE=$stagePath" "/DICON=$icon" "/DHELPER=$(Join-Path $PSScriptRoot 'installer-stop.ps1')" (Join-Path $PSScriptRoot 'installer-engine.iss')
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $engine)) { throw 'Installation engine build failed.' }
    New-Item -ItemType Directory -Path $configDir | Out-Null
    $frontendConfig = [ordered]@{ name='OpenDL Installer'; identifier='com.example.opendl.installer'; version=$Version; main='../cmd/installer'; icon='../resources/icon.png' } | ConvertTo-Json
    [IO.File]::WriteAllText((Join-Path $configDir 'mygo.json'), $frontendConfig, [Text.UTF8Encoding]::new($false))
    $frontendOutput = Join-Path $temp 'frontend-build'
    $env:OPENDL_SKIP_MYGO_INSTALLER = '1'
    & go tool mygo build -platform "windows/$Architecture" -o $frontendOutput $configDir
    if ($LASTEXITCODE -ne 0) { throw 'Installer interface build failed.' }
    $frontend = Join-Path $frontendOutput "windows-$Architecture/OpenDL Installer.exe"
    if (-not (Test-Path -LiteralPath $frontend)) { throw 'Installer interface executable is missing.' }
    Copy-Item -LiteralPath $frontend -Destination $packaged
    $stream = [IO.File]::Open($packaged, [IO.FileMode]::Append, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try {
        $payload = [IO.File]::OpenRead($engine)
        try {
            $payload.CopyTo($stream)
            $length = [BitConverter]::GetBytes([uint64]$payload.Length)
            if (-not [BitConverter]::IsLittleEndian) { [array]::Reverse($length) }
            $stream.Write($length, 0, $length.Length)
            $payload.Position = 0
            $sha256 = [Security.Cryptography.SHA256]::Create()
            try { $digest = $sha256.ComputeHash($payload) } finally { $sha256.Dispose() }
            $stream.Write($digest, 0, $digest.Length)
            $magic = [Text.Encoding]::ASCII.GetBytes('OPENDL-INSTALL-2')
            $stream.Write($magic, 0, $magic.Length)
        } finally { $payload.Dispose() }
    } finally { $stream.Dispose() }
    Move-Item -LiteralPath $packaged -Destination $outputPath -Force
} finally {
    $env:OPENDL_SKIP_MYGO_INSTALLER = $oldSkip
    Pop-Location
    if (Test-Path -LiteralPath $engine) { Remove-Item -LiteralPath $engine -Force }
    if (Test-Path -LiteralPath $configDir) {
        $configFile = Join-Path $configDir 'mygo.json'
        if (Test-Path -LiteralPath $configFile) { Remove-Item -LiteralPath $configFile -Force }
        Remove-Item -LiteralPath $configDir -Force
    }
    if (Test-Path -LiteralPath $temp) {
        $resolved = (Resolve-Path -LiteralPath $temp).Path
        $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
        if (-not $resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or
            -not [IO.Path]::GetFileName($resolved).StartsWith('opendl-package-')) { throw "Unexpected temporary path: $resolved" }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
