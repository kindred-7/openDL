param(
    [Parameter(Mandatory = $true)][string]$Installer,
    [Parameter(Mandatory = $true)][string]$Manifest,
    [Parameter(Mandatory = $true)][string]$NotesFile,
    [string]$Tag = '',
    [ValidateRange(1, 5)][int]$UploadAttempts = 3,
    [ValidateRange(120, 900)][int]$UploadTimeoutSec = 600
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
if (($payload.notes -replace "`r`n", "`n") -cne ($notes -replace "`r`n", "`n")) { throw 'Release notes do not match the signed manifest.' }
# Preserve the exact signed text when publishing; Git checkouts may change line endings.
$notes = [string]$payload.notes

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
function Get-GitCodeResource([string]$Uri, [string]$Description, [switch]$AllowMissing) {
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        try { return Invoke-RestMethod -Uri $Uri -TimeoutSec 60 }
        catch {
            $status = [int]$_.Exception.Response.StatusCode
            if ($status -eq 404 -and $AllowMissing) { return $null }
            if ($status -in @(401, 403)) { throw [UnauthorizedAccessException]::new("$Description failed (HTTP $status). Check the GitCode token permissions.") }
            if (($status -ge 400 -and $status -lt 500 -and $status -notin @(408, 425, 429)) -or $attempt -eq 3) {
                throw "$Description failed (HTTP $status) after $attempt attempt(s)."
            }
            $delay = 10 * $attempt
            $retryAfter = $_.Exception.Response.Headers.RetryAfter
            if ($retryAfter -and $retryAfter.Delta) { $delay = [math]::Min(60, [math]::Max($delay, [math]::Ceiling($retryAfter.Delta.TotalSeconds))) }
            Write-Warning "$Description failed (HTTP $status), attempt $attempt/3; retrying in ${delay}s."
            Start-Sleep -Seconds $delay
        }
    }
}
# Use curl's bounded connection and low-speed timeouts. Pass signed URLs and
# headers through stdin, never through process arguments or diagnostic output.
$curl = Get-Command curl.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $curl) { $curl = Get-Command curl -CommandType Application -ErrorAction Stop | Select-Object -First 1 }
function ConvertTo-CurlConfigValue([string]$Value) {
    if ($Value -match '[\r\n\x00]') { throw 'Invalid upload configuration value.' }
    return '"' + $Value.Replace('\', '\\').Replace('"', '\"') + '"'
}
function Send-ReleaseFile($Upload, [string]$Path) {
    $uploadUri = $null
    if (-not [Uri]::TryCreate([string]$Upload.url, [UriKind]::Absolute, [ref]$uploadUri) -or $uploadUri.Scheme -cne 'https') { throw 'GitCode returned an invalid upload URL.' }
    Write-Host "Upload endpoint: $($uploadUri.DnsSafeHost); connect limit: 30s; low-speed limit: 1 KiB/s for 90s; total limit: ${UploadTimeoutSec}s."
    $config = @(
        'silent', 'fail', 'globoff', 'proto = "=https"',
        'connect-timeout = 30', "max-time = $UploadTimeoutSec",
        'speed-limit = 1024', 'speed-time = 90',
        ('output = ' + (ConvertTo-CurlConfigValue $(if ($env:OS -eq 'Windows_NT') { 'NUL' } else { '/dev/null' }))),
        ('url = ' + (ConvertTo-CurlConfigValue $uploadUri.AbsoluteUri)),
        ('upload-file = ' + (ConvertTo-CurlConfigValue $Path)),
        'write-out = "%{http_code}|%{size_upload}|%{speed_upload}|%{time_namelookup}|%{time_connect}|%{time_appconnect}|%{time_starttransfer}|%{time_total}"'
    )
    $hasContentType = $false
    foreach ($property in $Upload.headers.PSObject.Properties) {
        if ($property.Name -notmatch '^[A-Za-z0-9-]+$') { throw 'Invalid upload header name.' }
        if ($property.Name -ieq 'Content-Type') { $hasContentType = $true }
        $config += 'header = ' + (ConvertTo-CurlConfigValue ($property.Name + ': ' + [string]$property.Value))
    }
    if (-not $hasContentType) { $config += 'header = "Content-Type: application/octet-stream"' }
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $curl.Source
    $start.Arguments = '--disable --config -'
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    try {
        [void]$process.Start()
        $output = $process.StandardOutput.ReadToEndAsync()
        $errors = $process.StandardError.ReadToEndAsync()
        $process.StandardInput.WriteLine(($config -join "`n"))
        $process.StandardInput.Close()
        $process.WaitForExit()
        $stats = $output.GetAwaiter().GetResult().Trim().Split('|')
        # Discard stderr: transport errors can include the signed URL.
        [void]$errors.GetAwaiter().GetResult()
        if ($stats.Count -ne 8 -or ($stats -join '|') -notmatch '^[0-9.|]+$') { throw 'Upload transport returned invalid diagnostics.' }
        Write-Host "Upload result: curl=$($process.ExitCode), HTTP=$($stats[0]), sent=$($stats[1]) bytes, average=$($stats[2]) bytes/s, DNS=$($stats[3])s, TCP=$($stats[4])s, TLS=$($stats[5])s, first response=$($stats[6])s, total=$($stats[7])s."
        return [pscustomobject]@{ ExitCode = $process.ExitCode; Status = [int]$stats[0] }
    } finally { $process.Dispose() }
}
function Test-ReleaseFile([string]$Path) {
    $name = [IO.Path]::GetFileName($Path)
    $remote = Get-GitCodeResource "${api}/tags/${tagEncoded}?$query" 'Inspecting GitCode assets'
    $assets = @($remote.assets | Where-Object { $_.name -ceq $name })
    if ($assets.Count -eq 0) { return $false }
    if ($assets.Count -ne 1) { throw [IO.InvalidDataException]::new("Duplicate GitCode asset: $name") }
    $localHash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash
    $digest = ([string]$assets[0].digest -replace '^sha256:', '').Trim()
    if ($digest -match '^[a-fA-F0-9]{64}$') {
        if ($digest -ine $localHash) { throw [IO.InvalidDataException]::new("Existing GitCode asset differs: $name. It will not be replaced.") }
        Write-Host "Verified existing asset: $name (server SHA-256 matches)."
        return $true
    }
    $temp = Join-Path ([IO.Path]::GetTempPath()) ('opendl-asset-' + [guid]::NewGuid().ToString('N'))
    try {
        $encoded = [Uri]::EscapeDataString($name)
        try { Invoke-WebRequest -Uri "${releasePath}/attach_files/${encoded}/download?$query" -OutFile $temp -TimeoutSec 180 | Out-Null }
        catch { throw "Failed to read existing GitCode asset: $name" }
        if ((Get-FileHash -LiteralPath $temp -Algorithm SHA256).Hash -cne $localHash) {
            throw [IO.InvalidDataException]::new("Existing GitCode asset differs: $name. It will not be replaced.")
        }
        Write-Host "Verified existing asset: $name (SHA-256 matches)."
        return $true
    } finally { Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue }
}
try {
    $body = @{ tag_name = $Tag; name = "OpenDL $version"; body = $notes; release_status = 'pre' } | ConvertTo-Json -Compress
    $existing = Get-GitCodeResource "${api}/tags/${tagEncoded}?$query" 'Inspecting the GitCode release' -AllowMissing
    if ($existing) {
        # GitCode 会在推送标签时自动生成仅有源码包的预发布：接管它并补全资产。
        $update = @{ name = "OpenDL $version"; body = $notes } | ConvertTo-Json -Compress
        try { Invoke-RestMethod -Method Patch -Uri "${releasePath}?$query" -ContentType 'application/json; charset=utf-8' -Body ([Text.Encoding]::UTF8.GetBytes($update)) -TimeoutSec 60 | Out-Null }
        catch { throw 'Failed to update the existing prerelease. Check the token permissions.' }
    } else {
        try { Invoke-RestMethod -Method Post -Uri "${api}?$query" -ContentType 'application/json; charset=utf-8' -Body ([Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 60 | Out-Null }
        catch { throw 'Failed to create the prerelease. Check the token permissions and whether this release already exists.' }
    }
    $created = $true
    foreach ($path in @($installerPath, $manifestPath)) {
        $name = [IO.Path]::GetFileName($path)
        $nameEncoded = [Uri]::EscapeDataString($name)
        $complete = $false
        for ($attempt = 1; $attempt -le $UploadAttempts; $attempt++) {
            Write-Host "Uploading $name ($((Get-Item -LiteralPath $path).Length) bytes), attempt $attempt/$UploadAttempts..."
            try {
                # Also handles a PUT that completed server-side after a client timeout.
                if (Test-ReleaseFile $path) { $complete = $true; break }
                $upload = Get-GitCodeResource "${releasePath}/upload_url?${query}&file_name=$nameEncoded" "Obtaining a fresh upload URL for $name"
                $result = Send-ReleaseFile $upload $path
                if ($result.ExitCode -eq 0 -and $result.Status -ge 200 -and $result.Status -lt 300) {
                    # Allow asset registration to become visible before a retry.
                    for ($poll = 0; $poll -lt 3; $poll++) {
                        if ($poll -gt 0) { Start-Sleep -Seconds (2 * $poll) }
                        if (Test-ReleaseFile $path) { $complete = $true; break }
                    }
                    if ($complete) { break }
                }
                if ($result.Status -ge 400 -and $result.Status -lt 500 -and $result.Status -notin @(401, 403, 408, 409, 425, 429)) {
                    throw [IO.InvalidDataException]::new("Upload rejected for $name (HTTP $($result.Status)); not retrying.")
                }
                throw "Upload did not complete for $name (curl=$($result.ExitCode), HTTP=$($result.Status))."
            } catch {
                if ($_.Exception -is [IO.InvalidDataException] -or $_.Exception -is [UnauthorizedAccessException]) { throw }
                Write-Warning $_.Exception.Message
                if ($attempt -lt $UploadAttempts) { Start-Sleep -Seconds (5 * [math]::Pow(2, $attempt - 1)) }
            }
        }
        # A final check avoids declaring failure when the last response was lost.
        if (-not $complete) { $complete = Test-ReleaseFile $path }
        if (-not $complete) { throw "Failed to upload $name after $UploadAttempts attempts. Rerun the GitCode publish job to reuse the saved signed files." }
    }
    $release = Get-GitCodeResource "${api}/tags/${tagEncoded}?$query" 'Reading uploaded release assets'
    $names = @($release.assets | ForEach-Object { $_.name })
    if ($names -cnotcontains $fileName -or $names -cnotcontains 'releases.json') { throw 'The release does not list both uploaded files.' }
    Write-Host 'Marking the release as latest...'
    $body = @{ name = "OpenDL $version"; body = $notes; release_status = 'latest' } | ConvertTo-Json -Compress
    try { Invoke-RestMethod -Method Patch -Uri "${releasePath}?$query" -ContentType 'application/json; charset=utf-8' -Body ([Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 60 | Out-Null }
    catch { throw 'Files were uploaded, but marking the release as latest failed.' }
    $published = Get-GitCodeResource "${api}/tags/${tagEncoded}?$query" 'Confirming final GitCode publication'
    if ($published.release_status -eq 'pre' -or $published.prerelease -eq $true) { throw 'Assets were retained, but GitCode still reports a prerelease. Retry publication without rebuilding.' }
    Write-Host "Published OpenDL ${version}: https://gitcode.com/kindred-wu/openDL/releases"
} catch {
    if ($created) { Write-Warning "Release $Tag and its uploaded assets were retained. A retry verifies existing files and uploads only missing files." }
    throw $_.Exception.Message
} finally {
    $token = $null
    $tokenEncoded = $null
    $secureToken = $null
}
