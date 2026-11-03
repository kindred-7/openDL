param(
    [Parameter(Mandatory)][ValidatePattern('^opendl-upload-diagnostic-[0-9]+$')][string]$Tag,
    [Parameter(Mandatory)][ValidateSet('system','git','linux','compat')][string]$Mode,
    [Parameter(Mandatory)][string]$Installer
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
if (-not $env:GITCODE_TOKEN) { throw 'Missing GitCode token.' }
$file = (Resolve-Path -LiteralPath $Installer).Path
if ((Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash -cne 'C8DF3B8068DD42DC748B78C95D2AB0AD2F75DF59D45EF0D6A882DA99CC2CA499') { throw 'Diagnostic installer differs from the official signed artifact.' }
if ($Mode -eq 'linux') {
    $curl = (Get-Command curl -CommandType Application | Select-Object -First 1).Source
} elseif ($Mode -eq 'git') {
    $curl = (Get-Command curl.exe -All -CommandType Application | Where-Object { $_.Source -match '\\Git\\' } | Select-Object -First 1).Source
} else { $curl = Join-Path $env:SystemRoot 'System32/curl.exe' }
if (-not $curl) { throw 'The requested curl implementation is unavailable.' }
Write-Host "Mode=$Mode; OS=$([Runtime.InteropServices.RuntimeInformation]::OSDescription); curl=$curl"
& $curl --version | Select-Object -First 2 | ForEach-Object { Write-Host $_ }
foreach ($name in @('HTTP_PROXY','HTTPS_PROXY','ALL_PROXY','NO_PROXY')) {
    Write-Host "$name configured: $(-not [string]::IsNullOrEmpty([Environment]::GetEnvironmentVariable($name)))"
}
function Quote-Config([string]$Value) {
    if ($Value -match '[\r\n\x00]') { throw 'Invalid curl configuration.' }
    return '"' + $Value.Replace('\','\\').Replace('"','\"') + '"'
}
$results = [Collections.Generic.List[object]]::new()
$query = 'access_token=' + [Uri]::EscapeDataString($env:GITCODE_TOKEN)
$api = "https://api.gitcode.com/api/v5/repos/kindred-wu/openDL/releases/$Tag"
$small = Join-Path $env:RUNNER_TEMP "upload-small-$Mode.bin"
$bytes = [byte[]]::new(256KB)
[Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
[IO.File]::WriteAllBytes($small, $bytes)
try {
    foreach ($path in @($small, $file)) {
        $label = if ($path -eq $small) { 'small' } else { 'installer' }
        $name = "$Mode-$label.bin"
        try { $upload = Invoke-RestMethod -Uri "${api}/upload_url?${query}&file_name=$name" -TimeoutSec 45 }
        catch { throw "Upload URL request failed (HTTP $([int]$_.Exception.Response.StatusCode))." }
        $uri = $null
        if (-not [Uri]::TryCreate([string]$upload.url, [UriKind]::Absolute, [ref]$uri) -or $uri.Scheme -ne 'https') { throw 'Invalid signed upload URL.' }
        Write-Host "Starting $label; bytes=$((Get-Item -LiteralPath $path).Length); endpoint=$($uri.DnsSafeHost)."
        $progress = Join-Path $env:RUNNER_TEMP "curl-$Mode-$label.log"
        $nullPath = if ($Mode -eq 'linux') { '/dev/null' } else { 'NUL' }
        $limit = if ($label -eq 'small') { 90 } else { 180 }
        $config = @('fail', 'globoff', 'proto = "=https"', 'connect-timeout = 20', "max-time = $limit",
            'speed-limit = 1024', 'speed-time = 30',
            ('stderr = ' + (Quote-Config $progress)),
            ('output = ' + (Quote-Config $nullPath)),
            ('url = ' + (Quote-Config $uri.AbsoluteUri)),
            ('upload-file = ' + (Quote-Config $path)),
            'write-out = "%{http_code}|%{size_upload}|%{speed_upload}|%{time_namelookup}|%{time_connect}|%{time_appconnect}|%{time_starttransfer}|%{time_total}|%{remote_ip}|%{http_version}"')
        foreach ($header in $upload.headers.PSObject.Properties) {
            if ($header.Name -notmatch '^[a-zA-Z0-9-]+$') { throw 'Invalid signed upload header.' }
            $config += 'header = ' + (Quote-Config ($header.Name + ': ' + [string]$header.Value))
        }
        if ($Mode -eq 'compat') { $config += @('ipv4', 'http1.1', 'tlsv1.2', 'tls-max = "1.2"', 'header = "Expect:"') }
        $start = [Diagnostics.ProcessStartInfo]::new()
        $start.FileName=$curl; $start.Arguments='--disable --config -'
        $start.UseShellExecute=$false; $start.CreateNoWindow=$true
        $start.RedirectStandardInput=$true; $start.RedirectStandardOutput=$true; $start.RedirectStandardError=$true
        $process = [Diagnostics.Process]::new()
        $process.StartInfo=$start
        $clock=[Diagnostics.Stopwatch]::StartNew()
        try {
            [void]$process.Start()
            $stdout=$process.StandardOutput.ReadToEndAsync()
            $stderr=$process.StandardError.ReadToEndAsync()
            $process.StandardInput.WriteLine(($config -join "`n"))
            $process.StandardInput.Close()
            while (-not $process.WaitForExit(5000)) {
                $row=''
                if (Test-Path -LiteralPath $progress) {
                    $rows=[IO.File]::ReadAllText($progress) -split '[\r\n]+'
                    $row=@($rows | Where-Object { $_ -match '^\s*[0-9]+\s' -and $_ -notmatch '[^0-9a-zA-Z% .:/+\-]' } | Select-Object -Last 1) -join ''
                }
                Write-Host ("{0} elapsed={1:N0}s; progress={2}" -f $label,$clock.Elapsed.TotalSeconds,$row.Trim())
                if ($clock.Elapsed.TotalSeconds -gt $limit+15) { $process.Kill(); $process.WaitForExit(); throw 'Curl exceeded its process watchdog.' }
            }
            $stats=$stdout.GetAwaiter().GetResult().Trim().Split('|')
            [void]$stderr.GetAwaiter().GetResult()
            if ($stats.Count -ne 10 -or ($stats -join '|') -notmatch '^[0-9a-fA-F:.|]+$') { throw 'Invalid sanitized transfer statistics.' }
            $record=[pscustomobject]@{Mode=$Mode;File=$label;CurlExit=$process.ExitCode;HTTP=$stats[0];Sent=$stats[1];BytesPerSecond=$stats[2];DNS=$stats[3];TCP=$stats[4];TLS=$stats[5];FirstResponse=$stats[6];Total=$stats[7];RemoteIP=$stats[8];HTTPVersion=$stats[9]}
            $results.Add($record)
            Write-Host ($record | ConvertTo-Json -Compress)
        } finally {
            if ($process.Id -and -not $process.HasExited) { $process.Kill(); $process.WaitForExit() }
            $process.Dispose()
            Remove-Item -LiteralPath $progress -Force -ErrorAction SilentlyContinue
        }
    }
} finally {
    $results.ToArray() | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath "upload-diagnostic-$Mode.json" -Encoding utf8
    Remove-Item -LiteralPath $small -Force -ErrorAction SilentlyContinue
    $query=$null; $upload=$null; $config=$null
}
