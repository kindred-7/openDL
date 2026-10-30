param([Parameter(Mandatory = $true)][string]$Executable, [switch]$ForceLegacy)
$ErrorActionPreference = 'Stop'
function Get-TargetProcesses([string]$Path) {
    # CIM exposes ExecutablePath across process bitness, unlike MainModule.Path
    # in a 32-bit PowerShell querying a 64-bit application.
    Get-CimInstance Win32_Process -Filter "Name = 'OpenDL.exe'" | Where-Object {
        $_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $Path, [StringComparison]::OrdinalIgnoreCase)
    }
}
try {
    $target = [IO.Path]::GetFullPath($Executable)
    if (-not [IO.File]::Exists($target)) { exit 0 }
    $tokenPath = Join-Path $env:APPDATA 'opendl\api-token'
    $exitRequested = $false
    if (Test-Path -LiteralPath $tokenPath) {
        try {
            $token = [IO.File]::ReadAllText($tokenPath).Trim()
            $body = @{ executable = $target } | ConvertTo-Json -Compress
            $response = Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:37913/api/installer-exit' -Headers @{ 'X-OpenDL-Token' = $token } -ContentType 'application/json' -Body $body -TimeoutSec 3
            $exitRequested = $response.ok -eq $true
        } catch { }
    }
    # The optional force path is used only after an explicit installer prompt.
    # Recheck the full executable path for every process before acting.
    if ($ForceLegacy -or -not $exitRequested) {
        foreach ($process in Get-TargetProcesses $target) {
            try {
                $current = Get-CimInstance Win32_Process -Filter "ProcessId = $($process.ProcessId)" -ErrorAction Stop
                if (-not $current -or -not [string]::Equals($current.ExecutablePath, $target, [StringComparison]::OrdinalIgnoreCase) -or $current.CreationDate -ne $process.CreationDate) { continue }
                $instance = Get-Process -Id $process.ProcessId -ErrorAction Stop
                if ($ForceLegacy) { $instance.Kill() }
                else { [void]$instance.CloseMainWindow() }
            } catch { }
        }
    }
    # Wait for the exact file, including processes without a visible window.
    # An exclusive open also detects locks held by other applications.
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        try {
            $file = [IO.File]::Open($target, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
            $file.Dispose()
            exit 0
        } catch [IO.IOException] { Start-Sleep -Milliseconds 500 }
          catch [UnauthorizedAccessException] { Start-Sleep -Milliseconds 500 }
    } while ([DateTime]::UtcNow -lt $deadline)
    if (-not $ForceLegacy) {
        if (@(Get-TargetProcesses $target).Count -gt 0) { exit 4 }
    }
    exit 2
} catch {
    Write-Output ("Exit check failed: " + $_.Exception.Message)
    exit 3
}
