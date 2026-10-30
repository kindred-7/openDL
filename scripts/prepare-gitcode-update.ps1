param(
    [Parameter(Mandatory = $true)][string]$Installer,
    [Parameter(Mandatory = $true)][string]$NotesFile,
    [ValidateSet('amd64', 'arm64')][string]$Architecture = 'amd64',
    [string]$OtherInstaller = '',
    [string]$PrivateKey = (Join-Path $env:USERPROFILE '.config\opendl-release-keys\private.key'),
    [string]$ExpectedPublicKey = '',
    [string]$Tag = '',
    [string]$OutputDirectory = '',
    [ValidateRange(1, 90)][int]$ValidDays = 90
)
$ErrorActionPreference = 'Stop'
& (Join-Path $PSScriptRoot 'prepare-github-update.ps1') -Provider gitcode -Repository 'kindred-wu/openDL' @PSBoundParameters
