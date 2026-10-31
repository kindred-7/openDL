param(
    [Parameter(Mandatory = $true)][string]$Installer,
    [Parameter(Mandatory = $true)][string]$NotesFile,
    [ValidateSet('amd64', 'arm64')][string]$Architecture = 'amd64',
    [string]$OtherInstaller = '',
    [string]$PrivateKey = (Join-Path $env:USERPROFILE '.config\opendl-release-keys\private.key'),
    [string]$ExpectedPublicKey = '',
    [ValidateSet('github', 'gitcode')][string]$Provider = 'github',
    [string]$Repository = 'kindred-7/openDL',
    [string]$Tag = '',
    [string]$OutputDirectory = '',
    [ValidateRange(1, 90)][int]$ValidDays = 90
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$installerPath = (Resolve-Path -LiteralPath $Installer).Path
$notesPath = (Resolve-Path -LiteralPath $NotesFile).Path
$keyPath = (Resolve-Path -LiteralPath $PrivateKey).Path
if (-not $ExpectedPublicKey) {
    $updaterSource = [IO.File]::ReadAllText((Join-Path $root 'cmd\opendl\page_update.go'))
    $match = [regex]::Match($updaterSource, '(?m)^var officialUpdatePublicKey = "([^"]+)"')
    if (-not $match.Success) { throw 'Cannot determine the application public key; provide -ExpectedPublicKey.' }
    $ExpectedPublicKey = $match.Groups[1].Value
}
try { $privateText = [IO.File]::ReadAllText($keyPath).Trim() }
catch { throw "Cannot read the release private key at $keyPath. Check its file permissions. $($_.Exception.Message)" }
try { $privateBytes = [Convert]::FromBase64String($privateText) }
catch { throw 'The release private key is not valid Base64.' }
$privateText = $null
if ($privateBytes.Length -ne 64 -or [Convert]::ToBase64String($privateBytes, 32, 32) -cne $ExpectedPublicKey.Trim()) {
    throw 'The release private key does not match the expected application public key.'
}
$privateBytes = $null
$config = Get-Content -LiteralPath (Join-Path $root 'mygo.json') -Raw | ConvertFrom-Json
$version = [string]$config.version
if ($version -notmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$') { throw 'mygo.json version must be a stable major.minor.patch version.' }
if ($Repository -notmatch '^[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$') { throw 'Repository must have the form owner/repository.' }
if (-not $Tag) { $Tag = "v$version" }
if ($Tag -cne $version -and $Tag -cne "v$version") { throw "Tag must be $version or v$version." }
$paths = @(@{ path = $installerPath; arch = $Architecture })
if ($OtherInstaller) {
    $otherPath = (Resolve-Path -LiteralPath $OtherInstaller).Path
    if ($otherPath -eq $installerPath -or [IO.Path]::GetFileName($otherPath) -eq [IO.Path]::GetFileName($installerPath)) { throw 'Installer assets must have distinct paths and filenames.' }
    $otherArch = if ($Architecture -eq 'amd64') { 'arm64' } else { 'amd64' }
    $paths += @{ path = $otherPath; arch = $otherArch }
}
$assets = @()
$assetArgs = @()
foreach ($item in $paths) {
    $filename = [IO.Path]::GetFileName($item.path)
    $expectedFilename = "OpenDL Setup $version $($item.arch).exe"
    $legacyFilename = "OpenDL.Setup.$version.$($item.arch).exe"
    if ($filename -cne $expectedFilename -and $filename -cne $legacyFilename) {
        throw "Expected $expectedFilename or $legacyFilename for $($item.arch), got $filename."
    }
    if ($Provider -eq 'gitcode') {
        $url = 'https://api.gitcode.com/api/v5/repos/' + $Repository + '/releases/' + [Uri]::EscapeDataString($Tag) + '/attach_files/' + [Uri]::EscapeDataString($filename) + '/download'
    } else {
        $url = 'https://github.com/' + $Repository + '/releases/download/' + [Uri]::EscapeDataString($Tag) + '/' + [Uri]::EscapeDataString($filename)
    }
    $assets += @{ os = 'windows'; arch = $item.arch; kind = 'installer'; url = $url; size = (Get-Item -LiteralPath $item.path).Length; sha256 = (Get-FileHash -LiteralPath $item.path -Algorithm SHA256).Hash.ToLowerInvariant() }
    $assetArgs += @('-asset', "windows/$($item.arch)/installer=$($item.path)")
}
$manifest = [ordered]@{
    schema = 1
    version = $version
    expires = [DateTime]::UtcNow.AddDays($ValidDays).ToString('yyyy-MM-ddTHH:mm:ssZ')
    notes = [IO.File]::ReadAllText($notesPath, [Text.Encoding]::UTF8)
    assets = $assets
}
if ($OutputDirectory) { $outputDir = (Resolve-Path -LiteralPath $OutputDirectory).Path }
else { $outputDir = Split-Path -Parent $installerPath }
$out = Join-Path $outputDir 'releases.json'
if (Test-Path -LiteralPath $out) { throw "Output already exists: $out" }
$draft = Join-Path ([IO.Path]::GetTempPath()) ('opendl-manifest-' + [Guid]::NewGuid().ToString('N') + '.json')
try {
    [IO.File]::WriteAllText($draft, ($manifest | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
    Push-Location $root
    try {
        & go run ./cmd/release-sign -key $keyPath -manifest $draft -out $out @assetArgs
        if ($LASTEXITCODE -ne 0) { throw 'Manifest signing failed.' }
    } finally { Pop-Location }
    Write-Host "Upload releases.json and all listed installers to $Provider release ${Tag}:"
    foreach ($item in $paths) { Write-Host "  $([IO.Path]::GetFileName($item.path))" }
    Write-Host "Signed manifest: $out"
} finally {
    if (Test-Path -LiteralPath $draft) { Remove-Item -LiteralPath $draft -Force }
}
