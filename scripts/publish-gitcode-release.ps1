param(
    [Parameter(Mandatory = $true)][string]$Installer,
    [Parameter(Mandatory = $true)][string]$Manifest,
    [Parameter(Mandatory = $true)][string]$NotesFile,
    [string]$Tag = ''
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$installerPath = (Resolve-Path -LiteralPath $Installer).Path
$manifestPath = (Resolve-Path -LiteralPath $Manifest).Path
$notesPath = (Resolve-Path -LiteralPath $NotesFile).Path
$version = [string](Get-Content -LiteralPath (Join-Path $root 'mygo.json') -Raw | ConvertFrom-Json).version
if ($version -notmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$') { throw 'Invalid project version.' }
if (-not $Tag) { $Tag = $version }
if ($Tag -cne $version -and $Tag -cne "v$version") { throw 'The tag must match the project version.' }
$fileName = [IO.Path]::GetFileName($installerPath)
if ($fileName -cne "OpenDL Setup $version amd64.exe" -and $fileName -cne "OpenDL.Setup.$version.amd64.exe") { throw 'The installer filename does not match the version and architecture.' }
if ([IO.Path]::GetFileName($manifestPath) -cne 'releases.json') { throw 'The manifest must be named releases.json.' }

$source = [IO.File]::ReadAllText((Join-Path $root 'cmd\opendl\page_update.go'))
$match = [regex]::Match($source, '(?m)^var officialUpdatePublicKey = "([^"]+)"')
if (-not $match.Success) { throw 'Cannot determine the application update public key.' }
$publicKeyFile = Join-Path ([IO.Path]::GetTempPath()) ('opendl-public-' + [Guid]::NewGuid().ToString('N') + '.txt')
try {
    [IO.File]::WriteAllText($publicKeyFile, $match.Groups[1].Value, [Text.UTF8Encoding]::new($false))
    Push-Location $root
    try {
        & go run ./cmd/release-sign -verify $manifestPath -public $publicKeyFile -asset "windows/amd64/installer=$installerPath"
        if ($LASTEXITCODE -ne 0) { throw 'The signed manifest or installer failed verification.' }
    } finally { Pop-Location }
} finally { Remove-Item -LiteralPath $publicKeyFile -Force -ErrorAction SilentlyContinue }

$envelope = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
$payload = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($envelope.payload)) | ConvertFrom-Json
$tagEncoded = [Uri]::EscapeDataString($Tag)
$expectedURL = 'https://api.gitcode.com/api/v5/repos/kindred-wu/openDL/releases/' + $tagEncoded + '/attach_files/' + [Uri]::EscapeDataString($fileName) + '/download'
if ($payload.version -cne $version -or @($payload.assets).Count -ne 1 -or $payload.assets[0].url -cne $expectedURL) { throw 'The signed manifest version or download URL does not match this release.' }
$notes = [IO.File]::ReadAllText($notesPath, [Text.Encoding]::UTF8)
if ($payload.notes -cne $notes) { throw 'Release notes do not match the signed manifest.' }

$env:GIT_TERMINAL_PROMPT = '0'
Write-Host "Checking remote tag $Tag..."
$remoteTag = & git -c credential.helper= ls-remote --tags https://gitcode.com/kindred-wu/openDL.git "refs/tags/$Tag"
if ($LASTEXITCODE -ne 0 -or -not $remoteTag) { throw "Remote tag $Tag was not found. Push the version tag before publishing." }
$secureToken = $null
if ($env:GITCODE_TOKEN) {
    $token = $env:GITCODE_TOKEN
} else {
    $secureToken = Read-Host 'GitCode access token (input is hidden)' -AsSecureString
    $token = [System.Net.NetworkCredential]::new('', $secureToken).Password
}
if (-not $token) { throw 'An access token is required.' }
$tokenEncoded = [Uri]::EscapeDataString($token)
$api = 'https://api.gitcode.com/api/v5/repos/kindred-wu/openDL/releases'
$releasePath = "$api/$tagEncoded"
$query = "access_token=$tokenEncoded"
$created = $false
try {
    $body = @{ tag_name = $Tag; name = "OpenDL $version"; body = $notes; release_status = 'pre' } | ConvertTo-Json -Compress
    $existing = $null
    try { $existing = Invoke-RestMethod -Method Get -Uri "${api}/tags/${tagEncoded}?$query" } catch { }
    if ($existing) {
        # GitCode 会在推送标签时自动生成仅有源码包的预发布：接管它并补全资产。
        $update = @{ name = "OpenDL $version"; body = $notes } | ConvertTo-Json -Compress
        try { Invoke-RestMethod -Method Patch -Uri "${releasePath}?$query" -ContentType 'application/json; charset=utf-8' -Body ([Text.Encoding]::UTF8.GetBytes($update)) | Out-Null }
        catch { throw 'Failed to update the existing prerelease. Check the token permissions.' }
    } else {
        try { Invoke-RestMethod -Method Post -Uri "${api}?$query" -ContentType 'application/json; charset=utf-8' -Body ([Text.Encoding]::UTF8.GetBytes($body)) | Out-Null }
        catch { throw 'Failed to create the prerelease. Check the token permissions and whether this release already exists.' }
    }
    $created = $true
    $names = @()
    try { $names = @((Invoke-RestMethod -Method Get -Uri "${api}/tags/${tagEncoded}?$query").assets | ForEach-Object { $_.name }) } catch { }
    foreach ($path in @($installerPath, $manifestPath)) {
        $name = [IO.Path]::GetFileName($path)
        if ($names -contains $name) { Write-Host "Already uploaded: $name"; continue }
        $nameEncoded = [Uri]::EscapeDataString($name)
        Write-Host "Uploading $name..."
        try { $upload = Invoke-RestMethod -Method Get -Uri "$releasePath/upload_url?${query}&file_name=$nameEncoded" }
        catch { throw "Failed to obtain an upload URL for $name." }
        $uploadUri = $null
        if (-not [Uri]::TryCreate([string]$upload.url, [UriKind]::Absolute, [ref]$uploadUri) -or $uploadUri.Scheme -cne 'https') { throw 'GitCode returned an invalid upload URL.' }
        $headers = @{}
        foreach ($property in $upload.headers.PSObject.Properties) {
            if ($property.Name -ine 'Content-Type') { $headers[$property.Name] = [string]$property.Value }
        }
        $contentType = [string]$upload.headers.'Content-Type'
        if (-not $contentType) { $contentType = 'application/octet-stream' }
        try { Invoke-WebRequest -Method Put -Uri $uploadUri.AbsoluteUri -Headers $headers -ContentType $contentType -InFile $path -TimeoutSec 600 | Out-Null }
        catch {
            # Do not print the exception message or request URL: the signed
            # upload URL and headers may contain credentials.
            $errorType = $_.Exception.GetType().FullName
            $status = if ($_.Exception.Response -and $_.Exception.Response.StatusCode) { " HTTP $([int]$_.Exception.Response.StatusCode)." } else { '' }
            throw "Failed to upload $name. Error type: $errorType.$status Upload timeout: 600 seconds."
        }
        Write-Host "Uploaded: $name"
    }
    try { $release = Invoke-RestMethod -Method Get -Uri "${api}/tags/${tagEncoded}?$query" }
    catch { throw 'Failed to read the uploaded release assets.' }
    $names = @($release.assets | ForEach-Object { $_.name })
    if ($names -cnotcontains $fileName -or $names -cnotcontains 'releases.json') { throw 'The release does not list both uploaded files.' }
    Write-Host 'Marking the release as latest...'
    $body = @{ name = "OpenDL $version"; body = $notes; release_status = 'latest' } | ConvertTo-Json -Compress
    try { Invoke-RestMethod -Method Patch -Uri "${releasePath}?$query" -ContentType 'application/json; charset=utf-8' -Body ([Text.Encoding]::UTF8.GetBytes($body)) | Out-Null }
    catch { throw 'Files were uploaded, but marking the release as latest failed.' }
    Write-Host "Published OpenDL ${version}: https://gitcode.com/kindred-wu/openDL/releases"
} catch {
    if ($created) { Write-Warning "A prerelease may exist for $Tag. Inspect it on GitCode before retrying; the script does not replace existing releases." }
    throw $_.Exception.Message
} finally {
    $token = $null
    $tokenEncoded = $null
    $secureToken = $null
}
