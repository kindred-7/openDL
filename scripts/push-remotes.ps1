param(
    [string]$Tag = ''
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$gitDir = Join-Path $root 'output\privacy-publish\.git'
if (-not (Test-Path -LiteralPath $gitDir)) {
    throw "Git repository not found: $gitDir"
}

$gitArgs = @('-c', 'core.safecrlf=false', "--git-dir=$gitDir", "--work-tree=$root")
$branch = (& git @gitArgs branch --show-current).Trim()
if ($LASTEXITCODE -ne 0 -or $branch -cne 'main') {
    throw 'Switch to the main branch before pushing.'
}
$status = @(& git @gitArgs status --porcelain --untracked-files=normal)
if ($LASTEXITCODE -ne 0) { throw 'Could not read repository status.' }
if ($status.Count -gt 0) {
    throw 'The working tree has uncommitted changes. Review and commit them before pushing.'
}

if ($Tag) {
    $config = Get-Content -LiteralPath (Join-Path $root 'mygo.json') -Raw | ConvertFrom-Json
    $version = [string]$config.version
    if ($version -notmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$' -or ($Tag -cne $version -and $Tag -cne "v$version")) {
        throw 'The tag must match the stable version in mygo.json.'
    }
    $headCommit = (& git @gitArgs rev-parse --verify 'HEAD^{commit}').Trim()
    if ($LASTEXITCODE -ne 0) { throw 'Could not resolve the current commit.' }
    & git @gitArgs show-ref --verify --quiet "refs/tags/$Tag"
    if ($LASTEXITCODE -eq 1) {
        Write-Host "Creating tag $Tag at the current commit..."
        & git @gitArgs tag $Tag $headCommit
        if ($LASTEXITCODE -ne 0) { throw 'Failed to create the version tag.' }
    } elseif ($LASTEXITCODE -ne 0) {
        throw 'Could not check whether the version tag exists.'
    }
    $tagCommit = (& git @gitArgs rev-parse --verify "refs/tags/$Tag^{commit}").Trim()
    if ($LASTEXITCODE -ne 0 -or $tagCommit -cne $headCommit) {
        throw 'The existing tag points to a different commit and will not be replaced.'
    }
}

foreach ($remote in @('gitcode', 'origin')) {
    Write-Host "Pushing main to $remote..."
    & git @gitArgs push $remote main
    if ($LASTEXITCODE -ne 0) { throw "Failed to push main to $remote. Later pushes were not attempted." }
}

if ($Tag) {
    foreach ($remote in @('gitcode', 'origin')) {
        Write-Host "Pushing tag $Tag to $remote..."
        & git @gitArgs push $remote "refs/tags/$Tag"
        if ($LASTEXITCODE -ne 0) { throw "Failed to push tag to $remote. Later pushes were not attempted; check the remote before retrying." }
    }
}

Write-Host 'Push completed.'
