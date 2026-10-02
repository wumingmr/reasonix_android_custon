[CmdletBinding()]
param(
  [string]$ApplicationPath,
  [string]$FixtureBuilderPath,
  [string]$ExpectedVersion,
  [string]$EvidenceDirectory
)
$ErrorActionPreference = 'Stop'

function Invoke-UpgradeFixture([string]$builder, [string[]]$arguments) {
  & $builder @arguments
  if ($LASTEXITCODE -ne 0) { throw "Upgrade fixture failed with exit code $LASTEXITCODE" }
}

function Invoke-UpgradeStartup([string]$installRoot, [string]$version, [string]$fixtureHome, [string]$text, [string]$evidence, [bool]$prepareHistorical) {
  & (Join-Path $PSScriptRoot 'test-windows-startup-recovery.ps1') `
    -InstallRoot $installRoot -ExpectedVersion $version -DataHome $fixtureHome `
    -ExpectedVisibleText $text -EvidenceDirectory $evidence -PrepareHistoricalSession:$prepareHistorical
  if (-not $?) { throw 'Upgraded startup verification failed.' }
}

function Invoke-WindowsUpgradeAcceptance {
  param(
    [Parameter(Mandatory=$true)][string]$ApplicationPath,
    [Parameter(Mandatory=$true)][string]$FixtureBuilderPath,
    [Parameter(Mandatory=$true)][string]$ExpectedVersion,
    [Parameter(Mandatory=$true)][string]$EvidenceDirectory
  )
  $application = (Resolve-Path -LiteralPath $ApplicationPath).Path
  $fixtureBuilder = (Resolve-Path -LiteralPath $FixtureBuilderPath).Path
  $installRoot = Split-Path $application -Parent
  $evidence = [IO.Path]::GetFullPath($EvidenceDirectory)
  if (Test-Path -LiteralPath $evidence) { throw 'Upgrade acceptance evidence directory must be new.' }
  $current = Get-Content -LiteralPath (Join-Path $installRoot 'current.json') -Raw | ConvertFrom-Json
  $release = Join-Path $installRoot $current.activeDir
  $source = Get-Content -LiteralPath (Join-Path $release 'app/resources/build.json') -Raw | ConvertFrom-Json
  if ($current.activeVersion -ne $ExpectedVersion -or $source.version -ne $ExpectedVersion) {
    throw 'Upgrade acceptance package version does not match the expected version.'
  }
  if ([string]$source.commit -notmatch '^[0-9a-fA-F]{40}$') {
    throw 'Packaged build.json must contain a full source commit before recording acceptance.'
  }
  New-Item -ItemType Directory -Path $evidence | Out-Null
  $fixtureHome = Join-Path $evidence 'home # %20 中文'
  $fixtureReport = Join-Path $evidence 'fixture.json'
  Invoke-UpgradeFixture $fixtureBuilder @('--mode', 'create', '--home', $fixtureHome, '--report', $fixtureReport)
  $fixture = Get-Content -LiteralPath $fixtureReport -Raw | ConvertFrom-Json
  if ([string]::IsNullOrWhiteSpace($fixture.visibleText)) { throw 'Fixture assistant body marker is missing.' }
  foreach ($phase in @('first', 'restart')) {
    Invoke-UpgradeStartup $installRoot $ExpectedVersion $fixtureHome $fixture.visibleText (Join-Path $evidence $phase) ($phase -eq 'restart')
    Invoke-UpgradeFixture $fixtureBuilder @('--mode', 'verify', '--home', $fixtureHome, '--report', $fixtureReport, '--phase', $phase)
  }
  @{
    version = $ExpectedVersion
    sourceSha = $source.commit
    applicationSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $application).Hash
    oldDataUpgrade = 'passed'
    restart = 'passed'
    visibleHistory = 'passed'
    registryBackup = 'passed'
    topicUnknownData = 'passed'
  } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $evidence 'result.json') -Encoding utf8
}

if ($MyInvocation.InvocationName -ne '.') { Invoke-WindowsUpgradeAcceptance @PSBoundParameters }
