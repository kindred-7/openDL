param(
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][string]$Notes
)

$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$') { throw 'Version must be a stable major.minor.patch version.' }
if ([string]::IsNullOrWhiteSpace($Notes)) { throw 'Release notes cannot be empty.' }
$root = Split-Path -Parent $PSScriptRoot
$gitDir = Join-Path $root 'output\privacy-publish\.git'
if (-not (Test-Path -LiteralPath $gitDir)) { throw "Git repository not found: $gitDir" }
$gitArgs = @('-c', 'core.safecrlf=false', "--git-dir=$gitDir", "--work-tree=$root")
$branch = (& git @gitArgs branch --show-current).Trim()
if ($LASTEXITCODE -ne 0 -or $branch -cne 'main') { throw 'Switch to main before releasing.' }

# Do not silently add new, unreviewed files, which may contain local secrets.
$untracked = @(& git @gitArgs ls-files --others --exclude-standard)
if ($LASTEXITCODE -ne 0) { throw 'Could not inspect untracked files.' }
$unreviewed = @($untracked | Where-Object { $_ -ne 'scripts/release.ps1' -and $_ -ne 'scripts/push-remotes.ps1' })
if ($unreviewed.Count -gt 0) {
    Write-Host 'Review and manually stage these new files before releasing:'
    $unreviewed | ForEach-Object { Write-Host "  $_" }
    throw 'Untracked files require review.'
}

$configPath = Join-Path $root 'mygo.json'
$configText = [IO.File]::ReadAllText($configPath)
$config = $configText | ConvertFrom-Json
$currentVersion = [string]$config.version
if ($currentVersion -notmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$') { throw 'Invalid current project version.' }
if ([version]$Version -lt [version]$currentVersion) { throw 'The release version cannot be lower than the current version.' }
$head = (& git @gitArgs rev-parse --verify 'HEAD^{commit}').Trim()
if ($LASTEXITCODE -ne 0) { throw 'Could not resolve HEAD.' }
& git @gitArgs show-ref --verify --quiet "refs/tags/$Version"
$localTagStatus = $LASTEXITCODE
if ($localTagStatus -eq 0) {
    $existingCommit = (& git @gitArgs rev-parse --verify "refs/tags/$Version^{commit}").Trim()
    if ($LASTEXITCODE -ne 0 -or $existingCommit -cne $head) { throw 'The local version tag points to a different commit.' }
    $dirty = @(& git @gitArgs status --porcelain --untracked-files=normal)
    if ($LASTEXITCODE -ne 0 -or $dirty.Count -gt 0) { throw 'This version is already tagged. Commit changes under a newer version instead.' }
} elseif ($localTagStatus -ne 1) { throw 'Could not inspect local version tags.' }
foreach ($remote in @('gitcode', 'origin')) {
    $remoteHead = @(& git @gitArgs ls-remote --heads $remote refs/heads/main)
    if ($LASTEXITCODE -ne 0 -or $remoteHead.Count -ne 1 -or $remoteHead[0] -notmatch '^([a-f0-9]{40})\s') { throw "Could not inspect $remote main." }
    $remoteCommit = $Matches[1]
    & git @gitArgs merge-base --is-ancestor $remoteCommit $head
    if ($LASTEXITCODE -ne 0) { throw "$remote main is not an ancestor of the local commit. Fetch and reconcile it first." }
    $refs = @(& git @gitArgs ls-remote --tags --refs $remote)
    if ($LASTEXITCODE -ne 0) { throw "Could not inspect $remote tags." }
    foreach ($ref in $refs) {
        if ($ref -notmatch '^([a-f0-9]{40})\s+refs/tags/(.+)$') { continue }
        $tagHash, $tagName = $Matches[1], $Matches[2]
        if ($tagName -match '^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$' -and [version]$tagName.TrimStart('v') -gt [version]$Version) {
            throw "$remote already has a newer version tag."
        }
        if (($tagName -ceq $Version -or $tagName -ceq "v$Version") -and ($tagName -cne $Version -or $tagHash -cne $head -or $localTagStatus -ne 0)) {
            throw "$remote already has a conflicting version tag. Existing tags are never replaced."
        }
    }
}

$notesPath = Join-Path $root "docs\release-notes-$Version.md"
$normalizedNotes = $Notes.Trim() + "`n"
if (Test-Path -LiteralPath $notesPath) {
    if ([IO.File]::ReadAllText($notesPath).Trim() -cne $Notes.Trim()) { throw 'The release notes file already exists with different content.' }
} else {
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $notesPath) | Out-Null
    [IO.File]::WriteAllText($notesPath, $normalizedNotes, [Text.UTF8Encoding]::new($false))
}
if ($currentVersion -cne $Version) {
    $pattern = '(?m)("version"\s*:\s*")' + [regex]::Escape($currentVersion) + '(")'
    $matches = [regex]::Matches($configText, $pattern)
    if ($matches.Count -ne 1) { throw 'Could not safely update the project version.' }
    $updated = [regex]::Replace($configText, $pattern, { param($m) $m.Groups[1].Value + $Version + $m.Groups[2].Value })
    [IO.File]::WriteAllText($configPath, $updated, [Text.UTF8Encoding]::new($false))
}

# Include tracked changes and the release scripts; new unrelated files must be
# staged explicitly by the operator before running the script.
& git @gitArgs add -u
if ($LASTEXITCODE -ne 0) { throw 'Failed to stage tracked changes.' }
foreach ($path in @('scripts/release.ps1', 'scripts/push-remotes.ps1')) {
    if (Test-Path -LiteralPath (Join-Path $root $path)) {
        & git @gitArgs add -- $path
        if ($LASTEXITCODE -ne 0) { throw "Failed to stage $path." }
    }
}
& git @gitArgs add -f -- "docs/release-notes-$Version.md"
if ($LASTEXITCODE -ne 0) { throw 'Failed to stage release notes.' }
& git @gitArgs diff --cached --quiet --exit-code
$diffCode = $LASTEXITCODE
if ($diffCode -eq 1) {
    & git @gitArgs commit -m "chore: release OpenDL $Version"
    if ($LASTEXITCODE -ne 0) { throw 'Commit failed; check the index and retry.' }
} elseif ($diffCode -ne 0) { throw 'Could not inspect staged changes.' }

$head = (& git @gitArgs rev-parse --verify 'HEAD^{commit}').Trim()
if ($LASTEXITCODE -ne 0) { throw 'Could not resolve the release commit.' }
& git @gitArgs show-ref --verify --quiet "refs/tags/$Version"
if ($LASTEXITCODE -eq 1) {
    & git @gitArgs tag $Version $head
    if ($LASTEXITCODE -ne 0) { throw 'Could not create the release tag.' }
} elseif ($LASTEXITCODE -ne 0) { throw 'Could not inspect the local tag.' }
$tagHead = (& git @gitArgs rev-parse --verify "refs/tags/$Version^{commit}").Trim()
if ($LASTEXITCODE -ne 0 -or $tagHead -cne $head) { throw 'The local tag points to a different commit; it will not be replaced.' }

foreach ($remote in @('gitcode', 'origin')) {
    Write-Host "Pushing main to $remote..."
    & git @gitArgs push $remote main
    if ($LASTEXITCODE -ne 0) { throw "Push to $remote failed. Inspect the remote before retrying." }
}
foreach ($remote in @('gitcode', 'origin')) {
    Write-Host "Pushing tag $Version to $remote..."
    & git @gitArgs push $remote "refs/tags/$Version"
    if ($LASTEXITCODE -ne 0) { throw "Tag push to $remote failed. Inspect the remote before retrying." }
}
Write-Host "Release $Version queued. Check GitHub Actions for the final publication result."
