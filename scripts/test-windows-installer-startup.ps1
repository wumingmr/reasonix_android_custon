[CmdletBinding()]
param(
  [Parameter(Mandatory=$true)][string]$InstallerPath,
  [Parameter(Mandatory=$true)][string]$ExpectedVersion,
  [string]$FixtureBuilderPath = '',
  [string]$EvidenceDirectory = (Join-Path $env:TEMP ('reasonix-installer-' + [guid]::NewGuid().ToString('N'))),
  [switch]$DisposableEnvironment
)

. (Join-Path $PSScriptRoot 'windows-acceptance-environment.ps1')

function Get-DefaultReasonixDataHome {
  return (Join-Path $env:APPDATA 'reasonix')
}

function Get-InstallerIntegrationPaths {
  (Join-Path ([Environment]::GetFolderPath('DesktopDirectory', 'DoNotVerify')) 'Reasonix.lnk')
  (Join-Path ([Environment]::GetFolderPath('Programs', 'DoNotVerify')) 'Reasonix.lnk')
  foreach ($hive in @('HKCU:', 'HKLM:')) {
    foreach ($software in @('Software', 'Software\WOW6432Node')) {
      foreach ($product in @('ReasonixReasonix', 'Reasonix')) {
        "$hive\$software\Microsoft\Windows\CurrentVersion\Uninstall\$product"
      }
    }
  }
}

function Assert-InstallerTestAccount {
  param([switch]$DisposableEnvironment)
  if ($env:OS -ne 'Windows_NT') { throw 'Installer acceptance requires Windows.' }
  $hostedRunner = $env:GITHUB_ACTIONS -eq 'true' -and $env:RUNNER_ENVIRONMENT -eq 'github-hosted'
  if (-not $hostedRunner -and -not $DisposableEnvironment) {
    throw 'Use a disposable Windows user or VM snapshot and pass -DisposableEnvironment. Installation changes account-wide shortcuts and registration.'
  }

  # /D isolates payload files only. NSIS still owns account-wide registration,
  # shortcuts and legacy WebView data, including during uninstall.
  $protectedPaths = @(Get-InstallerIntegrationPaths) + @(
    (Join-Path $env:LOCALAPPDATA 'Programs\Reasonix'),
    (Join-Path $env:APPDATA 'reasonix-desktop.exe'),
    (Get-DefaultReasonixDataHome)
  )
  foreach ($path in $protectedPaths) {
    if (Test-Path -LiteralPath $path) {
      throw 'Existing Reasonix installation, shortcut or legacy data detected; use a clean disposable Windows account.'
    }
  }
  if (@(Get-Process -Name Reasonix,reasonix-desktop,reasonix-launcher -ErrorAction SilentlyContinue).Count -ne 0) {
    throw 'Reasonix is running; use a clean disposable Windows account.'
  }
}

function Install-Reasonix {
  param([string]$Installer, [string]$InstallRoot)
  $process = Start-Process -FilePath $Installer -ArgumentList @('/S', "/D=$InstallRoot") -PassThru -Wait
  if ($process.ExitCode -ne 0) { throw "Installer failed with exit code $($process.ExitCode)" }
}

function Assert-InstalledIdentity {
  param([string]$InstallRoot, [string]$ExpectedVersion)
  $currentPath = Join-Path $InstallRoot 'current.json'
  $current = Get-Content -LiteralPath $currentPath -Raw | ConvertFrom-Json
  $expectedDir = "versions/$ExpectedVersion"
  if ($current.schemaVersion -ne 1 -or $current.activeVersion -ne $ExpectedVersion -or $current.activeDir.Replace('\', '/') -ne $expectedDir) {
    throw "Installed current.json does not preserve release identity: $($current | ConvertTo-Json -Compress)"
  }

  $releaseDir = Join-Path $InstallRoot ($current.activeDir -replace '/', [IO.Path]::DirectorySeparatorChar)
  if (-not (Test-Path -LiteralPath $releaseDir -PathType Container)) {
    throw "Installed release directory is missing: $releaseDir"
  }
  $build = Get-Content -LiteralPath (Join-Path $releaseDir 'app\resources\build.json') -Raw | ConvertFrom-Json
  if ($build.version -ne $ExpectedVersion) {
    throw "Packaged shell identity does not match installed identity: build.json=$($build.version), current.json=$($current.activeVersion)"
  }
  $registration = Get-ItemProperty -LiteralPath 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\ReasonixReasonix'
  if ($registration.DisplayVersion -ne $ExpectedVersion.Substring(1) -or $registration.InstallLocation -ne $InstallRoot) {
    throw 'Uninstall registration does not match the tested version and installation.'
  }
  return @{ Current = $current; ReleaseDir = $releaseDir }
}

function Invoke-InstalledRuntimeAcceptance {
  param([string]$InstallRoot, [string]$ExpectedVersion, [string]$ArtifactPath, [string]$EvidenceDirectory)
  & (Join-Path $PSScriptRoot 'test-windows-startup-recovery.ps1') @PSBoundParameters | Out-Host
}

function Assert-UninstalledState {
  param([string]$InstallRoot, [hashtable]$DataSnapshot)
  # The activation lock or user-owned files may legitimately keep the root
  # directory nonempty. Every program entry and version payload must be gone.
  foreach ($name in @('versions', 'app', 'current.json', 'Reasonix.exe', 'reasonix-launcher.exe',
      'reasonix-desktop.exe', 'reasonix-cli.exe', 'reasonix-update-helper.exe', 'reasonix-guard.exe', 'uninstall.exe')) {
    if (Test-Path -LiteralPath (Join-Path $InstallRoot $name)) {
      throw "Uninstall left program files: $name"
    }
  }
  foreach ($path in Get-InstallerIntegrationPaths) {
    if (Test-Path -LiteralPath $path) { throw 'Uninstall left a shortcut or registration.' }
  }
  if (@(Get-Process -Name Reasonix,reasonix-desktop,reasonix-launcher -ErrorAction SilentlyContinue).Count -ne 0) {
    throw 'Uninstall left Reasonix processes running.'
  }
  foreach ($path in $DataSnapshot.Keys) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or
        (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash -ne $DataSnapshot[$path]) {
      throw 'Uninstall removed or changed preserved user data.'
    }
  }
}

function Invoke-WindowsInstallerAcceptance {
  param([string]$InstallerPath, [string]$ExpectedVersion, [string]$FixtureBuilderPath, [string]$EvidenceDirectory, [switch]$DisposableEnvironment)
  $ErrorActionPreference = 'Stop'
  if ($ExpectedVersion -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$') {
    throw "ExpectedVersion is not a canonical Reasonix version: $ExpectedVersion"
  }
  # Reject before creating fixtures or launching any installer, even with the
  # explicit disposable-environment flag. Never back up/overwrite a real install.
  Assert-InstallerTestAccount -DisposableEnvironment:$DisposableEnvironment
  $installer = (Resolve-Path -LiteralPath $InstallerPath).Path
  $artifactHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $installer).Hash
  $EvidenceDirectory = [IO.Path]::GetFullPath($EvidenceDirectory)
  if (Test-Path -LiteralPath $EvidenceDirectory) {
    throw 'Installer acceptance directory must be new; refusing to overwrite an existing fixture.'
  }
  New-Item -ItemType Directory -Path $EvidenceDirectory | Out-Null
  $install = Join-Path $EvidenceDirectory 'installed'

  $acceptanceEnvironment = Enter-WindowsAcceptanceEnvironment -DataHome (Join-Path $EvidenceDirectory 'installation-home')
  try {
    # Include the default user-data location: NSIS may use $APPDATA directly
    # even while the app uses an explicit isolated home. Preflight above refuses
    # existing default data; this sentinel belongs only to the disposable account.
    $defaultHome = Get-DefaultReasonixDataHome
    New-Item -ItemType Directory -Path $defaultHome | Out-Null
    $defaultSentinel = Join-Path $defaultHome 'acceptance-retention.txt'
    [IO.File]::WriteAllText($defaultSentinel, [guid]::NewGuid().ToString('N'))
    $defaultHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $defaultSentinel).Hash
    Install-Reasonix -Installer $installer -InstallRoot $install
    $identity = Assert-InstalledIdentity -InstallRoot $install -ExpectedVersion $ExpectedVersion
    Invoke-InstalledRuntimeAcceptance -InstallRoot $install -ExpectedVersion $ExpectedVersion -ArtifactPath $installer `
      -EvidenceDirectory (Join-Path $EvidenceDirectory 'fresh-install')

    $repair = 'not applicable'
    $truncatedVersion = $ExpectedVersion -replace '-.*$', ''
    if ($truncatedVersion -ne $ExpectedVersion) {
      # The first installed instance has passed startup AND clean exit before
      # changing the fixture. Keep the two acceptance reports independent.
      Move-Item -LiteralPath $identity.ReleaseDir -Destination (Join-Path $install "versions\$truncatedVersion")
      $pointer = @{
        schemaVersion = 1
        activeVersion = $truncatedVersion
        activeDir = "versions/$truncatedVersion"
      } | ConvertTo-Json
      [IO.File]::WriteAllText((Join-Path $install 'current.json'), $pointer, [Text.UTF8Encoding]::new($false))
      Install-Reasonix -Installer $installer -InstallRoot $install
      $identity = Assert-InstalledIdentity -InstallRoot $install -ExpectedVersion $ExpectedVersion
      Invoke-InstalledRuntimeAcceptance -InstallRoot $install -ExpectedVersion $ExpectedVersion -ArtifactPath $installer `
        -EvidenceDirectory (Join-Path $EvidenceDirectory 'repaired-install')
      $repair = 'passed'
    }

    $oldDataUpgrade = 'not requested'
    if ($FixtureBuilderPath) {
      & (Join-Path $PSScriptRoot 'test-windows-upgrade-startup.ps1') `
        -ApplicationPath (Join-Path $install 'Reasonix.exe') -FixtureBuilderPath $FixtureBuilderPath `
        -ExpectedVersion $ExpectedVersion -EvidenceDirectory (Join-Path $EvidenceDirectory 'old-data-upgrade')
      if ($LASTEXITCODE -ne 0) { throw "Old-data upgrade acceptance failed with exit code $LASTEXITCODE" }
      $oldDataUpgrade = 'passed'
    }

    # Failure preserves the fixture/logs for diagnosis. Uninstall only after
    # every tested instance exited normally, never while a failed shell lives.
    $dataSnapshot = @{ $defaultSentinel = $defaultHash }
    $homes = @($env:REASONIX_HOME, (Join-Path $EvidenceDirectory 'fresh-install\home'))
    if ($repair -eq 'passed') { $homes += (Join-Path $EvidenceDirectory 'repaired-install\home') }
    if ($oldDataUpgrade -eq 'passed') { $homes += (Join-Path $EvidenceDirectory 'old-data-upgrade\home # %20 中文') }
    foreach ($homePath in $homes) {
      New-Item -ItemType Directory -Force -Path $homePath | Out-Null
      [IO.File]::WriteAllText((Join-Path $homePath 'acceptance-retention.txt'), [guid]::NewGuid().ToString('N'))
      foreach ($file in Get-ChildItem -LiteralPath $homePath -Recurse -File -Force) {
        $dataSnapshot[$file.FullName] = (Get-FileHash -Algorithm SHA256 -LiteralPath $file.FullName).Hash
      }
    }
    $dataSnapshot | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $EvidenceDirectory 'data-before-uninstall.json') -Encoding utf8
    $uninstaller = Join-Path $install 'uninstall.exe'
    if (-not (Test-Path -LiteralPath $uninstaller -PathType Leaf)) { throw 'Installed uninstaller is missing.' }
    $uninstall = Start-Process -FilePath $uninstaller -ArgumentList '/S' -PassThru -Wait
    if ($uninstall.ExitCode -ne 0) { throw "Uninstaller failed with exit code $($uninstall.ExitCode)" }
    Assert-UninstalledState -InstallRoot $install -DataSnapshot $dataSnapshot
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $installer).Hash -ne $artifactHash) {
      throw 'Installer bytes changed during acceptance; refusing to attest this artifact.'
    }
    @{
      artifact = $artifactHash
      version = $ExpectedVersion
      freshInstall = 'passed'
      repairedInstall = $repair
      oldDataUpgrade = $oldDataUpgrade
      uninstall = 'passed'
      dataPreserved = 'passed'
      repairFrom = if ($repair -eq 'passed') { $truncatedVersion } else { $null }
    } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $EvidenceDirectory 'acceptance.json') -Encoding utf8
  } finally {
    Restore-WindowsAcceptanceEnvironment -Snapshot $acceptanceEnvironment
    Write-Host "Installer acceptance evidence: $EvidenceDirectory"
  }
}

# Dot-sourcing exposes the real orchestration for tests that replace only OS
# process/registry boundaries; it never runs an installation.
if ($MyInvocation.InvocationName -ne '.') {
  Invoke-WindowsInstallerAcceptance -InstallerPath $InstallerPath -ExpectedVersion $ExpectedVersion `
    -FixtureBuilderPath $FixtureBuilderPath -EvidenceDirectory $EvidenceDirectory -DisposableEnvironment:$DisposableEnvironment
}
