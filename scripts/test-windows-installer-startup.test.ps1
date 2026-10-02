# Exercise the real acceptance orchestration with disposable files and mocked
# process/registry boundaries. This never installs or uninstalls an application.
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'Run these tests on Windows.' }
. (Join-Path $PSScriptRoot 'test-windows-installer-startup.ps1') -InstallerPath unused -ExpectedVersion v1.2.3

$script:testRoot = [IO.Path]::GetFullPath((Join-Path $env:TEMP ('reasonix-installer-tests-' + [guid]::NewGuid().ToString('N'))))
$null = [IO.Directory]::CreateDirectory($script:testRoot)
$script:occupiedPath = ''
$script:running = $false
$script:events = [Collections.Generic.List[string]]::new()
$script:testCount = 0

function Assert-Equal($actual, $expected) {
  if ($actual -cne $expected) { throw "Expected [$expected], got [$actual]" }
}
function Assert-Throws([scriptblock]$Action, [string]$Message) {
  try { & $Action } catch {
    if ($_.Exception.Message -notlike "*$Message*") { throw }
    return
  }
  throw "Expected failure containing: $Message"
}
function Get-DefaultReasonixDataHome {
  # This OS boundary is redirected to the test fixture, never the VM account.
  return (Join-Path $script:testRoot "default-data-$script:testCount")
}
function Test-Path {
  param([string]$Path, [string]$LiteralPath, [string]$PathType)
  $candidate = if ($PSBoundParameters.ContainsKey('LiteralPath')) { $LiteralPath } else { $Path }
  if ([string]::IsNullOrEmpty($candidate)) { return $false }
  if ($candidate -eq $script:occupiedPath) { return $true }
  if ($candidate.StartsWith($script:testRoot, [StringComparison]::OrdinalIgnoreCase)) {
    $native = @{}
    if ($PSBoundParameters.ContainsKey('LiteralPath')) { $native.LiteralPath = $candidate } else { $native.Path = $candidate }
    if ($PSBoundParameters.ContainsKey('PathType')) { $native.PathType = $PathType }
    return Microsoft.PowerShell.Management\Test-Path @native
  }
  # All account state is virtual, including on a developer's existing VM.
  return $candidate -eq $script:occupiedPath
}
function Get-Process {
  param($Name, $ErrorAction)
  if ($script:running) { [pscustomobject]@{ Id = 123 } }
}
function Get-ItemProperty {
  param($LiteralPath)
  return [pscustomobject]@{ DisplayVersion = $script:version.Substring(1); InstallLocation = $script:installRoot }
}
function Assert-IsolatedEnvironment([string]$HomePath) {
  $HomePath = [IO.Path]::GetFullPath($HomePath)
  Assert-Equal $env:REASONIX_HOME $HomePath
  Assert-Equal $env:REASONIX_STATE_HOME $HomePath
  Assert-Equal $env:REASONIX_CACHE_HOME (Join-Path $HomePath 'cache')
  Assert-Equal $env:REASONIX_NONINTERACTIVE '1'
  foreach ($key in @('REASONIX_DEV','REASONIX_DESKTOP_SERVICE','REASONIX_ELECTRON_DEV_URL',
      'REASONIX_UNKNOWN_TEST_OVERRIDE','NODE_OPTIONS','ELECTRON_RUN_AS_NODE')) {
    Assert-Equal ([string][Environment]::GetEnvironmentVariable($key, 'Process')) ''
  }
}
function Assert-ParentEnvironment {
  Assert-Equal $env:REASONIX_HOME 'untouched-user-home'
  Assert-Equal $env:REASONIX_STATE_HOME 'untouched-user-state'
  Assert-Equal $env:REASONIX_CACHE_HOME 'untouched-user-cache'
  Assert-Equal $env:REASONIX_DEV 'untouched-dev-mode'
  Assert-Equal $env:REASONIX_UNKNOWN_TEST_OVERRIDE 'untouched-override'
  Assert-Equal $env:NODE_OPTIONS 'untouched-node-options'
  Assert-Equal $env:ELECTRON_RUN_AS_NODE '1'
}
function Start-Process {
  param([string]$FilePath, $ArgumentList, [switch]$PassThru, [switch]$Wait)
  if (-not $FilePath.StartsWith($script:testRoot)) { throw 'Process boundary escaped the fixture.' }
  Assert-IsolatedEnvironment (Join-Path $script:evidence 'installation-home')
  if ((Split-Path $FilePath -Leaf) -eq 'uninstall.exe') {
    $script:events.Add('uninstall')
    if ($script:uninstallFault -eq 'noop') { return [pscustomobject]@{ ExitCode = 0 } }
    [IO.Directory]::Delete($script:installRoot, $true)
    if ($script:uninstallFault -eq 'program') {
      $null = [IO.Directory]::CreateDirectory($script:installRoot)
      [IO.File]::WriteAllText((Join-Path $script:installRoot 'Reasonix.exe'), 'leftover')
    } elseif ($script:uninstallFault -eq 'shortcut') {
      $script:occupiedPath = Join-Path ([Environment]::GetFolderPath('Programs', 'DoNotVerify')) 'Reasonix.lnk'
    } elseif ($script:uninstallFault -eq 'registration') {
      $script:occupiedPath = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\ReasonixReasonix'
    } elseif ($script:uninstallFault -eq 'process') {
      $script:running = $true
    } elseif ($script:uninstallFault -eq 'default-data') {
      [IO.Directory]::Delete((Get-DefaultReasonixDataHome), $true)
    } elseif ($script:uninstallFault -eq 'runtime-data') {
      [IO.Directory]::Delete((Join-Path $script:evidence 'fresh-install\home'), $true)
    } elseif ($script:uninstallFault -eq 'modified-data') {
      [IO.File]::WriteAllText((Join-Path $script:evidence 'fresh-install\home\conversation.fixture'), 'corrupted')
    }
  } else {
    $script:events.Add('install')
    $script:installCount++
    $script:installRoot = $ArgumentList[1].Substring(3)
    if ($script:installCount -eq 2) {
      $bytes = [IO.File]::ReadAllBytes((Join-Path $script:installRoot 'current.json'))
      Assert-Equal ([int]$bytes[0]) 123 # '{', never a PowerShell 5 UTF-8 BOM
      $pointer = [Text.Encoding]::UTF8.GetString($bytes) | ConvertFrom-Json
      Assert-Equal $pointer.activeVersion ($script:version -replace '-.*$', '')
    }
    $releaseDir = Join-Path $script:installRoot ('versions\' + $script:version)
    $null = [IO.Directory]::CreateDirectory((Join-Path $releaseDir 'app\resources'))
    $pointer = @{ schemaVersion = 1; activeVersion = $script:version; activeDir = "versions/$script:version" } | ConvertTo-Json
    [IO.File]::WriteAllText((Join-Path $script:installRoot 'current.json'), $pointer)
    [IO.File]::WriteAllText((Join-Path $releaseDir 'app\resources\build.json'), (@{ version = $script:version } | ConvertTo-Json))
    [IO.File]::WriteAllText((Join-Path $script:installRoot 'uninstall.exe'), 'mock')
  }
  return [pscustomobject]@{ ExitCode = 0 }
}
function Invoke-InstalledRuntimeAcceptance {
  param([string]$InstallRoot, [string]$ExpectedVersion, [string]$ArtifactPath, [string]$EvidenceDirectory)
  $phase = Split-Path $EvidenceDirectory -Leaf
  $script:events.Add($phase)
  Assert-Equal $script:installCount $(if ($phase -eq 'fresh-install') { 1 } else { 2 })
  if ($script:failPhase -eq $phase) { throw "runtime failed: $phase" }
  $null = [IO.Directory]::CreateDirectory($EvidenceDirectory)
  $null = [IO.Directory]::CreateDirectory((Join-Path $EvidenceDirectory 'home'))
  [IO.File]::WriteAllText((Join-Path $EvidenceDirectory 'home\conversation.fixture'), 'preserve these fixture bytes')
  [IO.File]::WriteAllText((Join-Path $EvidenceDirectory 'result.json'), '{"mock":true}')
  if ($script:changeArtifact) { [IO.File]::AppendAllText($ArtifactPath, 'changed') }
}

function New-TestCase([string]$Version = 'v1.38.9-2') {
  $script:testCount++
  $script:version = $Version
  $script:evidence = Join-Path $script:testRoot "evidence-$script:testCount"
  $script:artifact = Join-Path $script:testRoot "installer-$script:testCount.exe"
  [IO.File]::WriteAllText($script:artifact, 'mock installer')
  $script:events.Clear()
  $script:installCount = 0
  $script:failPhase = ''
  $script:changeArtifact = $false
  $script:occupiedPath = ''
  $script:running = $false
  $script:uninstallFault = ''
}
function Invoke-TestCase {
  Invoke-WindowsInstallerAcceptance -InstallerPath $script:artifact -ExpectedVersion $script:version `
    -EvidenceDirectory $script:evidence -DisposableEnvironment
}

$saved = Get-WindowsAcceptanceEnvironment
foreach ($key in @('GITHUB_ACTIONS','RUNNER_ENVIRONMENT')) {
  $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
try {
  $env:GITHUB_ACTIONS = 'false'
  $env:RUNNER_ENVIRONMENT = 'self-hosted'
  $env:REASONIX_HOME = 'untouched-user-home'
  $env:REASONIX_DEV = 'untouched-dev-mode'
  $env:REASONIX_STATE_HOME = 'untouched-user-state'
  $env:REASONIX_CACHE_HOME = 'untouched-user-cache'
  $env:REASONIX_UNKNOWN_TEST_OVERRIDE = 'untouched-override'
  $env:NODE_OPTIONS = 'untouched-node-options'
  $env:ELECTRON_RUN_AS_NODE = '1'
  New-TestCase
  Assert-Throws { Invoke-WindowsInstallerAcceptance -InstallerPath $script:artifact -ExpectedVersion $script:version -EvidenceDirectory $script:evidence } 'disposable Windows'
  Assert-Equal $script:events.Count 0
  Assert-Equal (Test-Path -LiteralPath $script:evidence) $false

  $protected = @(
    (Join-Path ([Environment]::GetFolderPath('DesktopDirectory', 'DoNotVerify')) 'Reasonix.lnk'),
    (Join-Path ([Environment]::GetFolderPath('Programs', 'DoNotVerify')) 'Reasonix.lnk'),
    (Join-Path $env:LOCALAPPDATA 'Programs\Reasonix'),
    (Join-Path $env:APPDATA 'reasonix-desktop.exe')
  )
  foreach ($hive in @('HKCU:', 'HKLM:')) {
    foreach ($software in @('Software', 'Software\WOW6432Node')) {
      foreach ($product in @('ReasonixReasonix', 'Reasonix')) {
        $protected += "$hive\$software\Microsoft\Windows\CurrentVersion\Uninstall\$product"
      }
    }
  }
  foreach ($path in $protected) {
    New-TestCase
    $script:occupiedPath = $path
    Assert-Throws { Invoke-TestCase } 'Existing Reasonix'
    Assert-Equal $script:events.Count 0
    Assert-Equal (Test-Path -LiteralPath $script:evidence) $false
  }
  New-TestCase
  $script:occupiedPath = Get-DefaultReasonixDataHome
  Assert-Throws { Invoke-TestCase } 'Existing Reasonix'
  Assert-Equal $script:events.Count 0
  Assert-Equal (Test-Path -LiteralPath $script:evidence) $false
  New-TestCase
  $script:running = $true
  Assert-Throws { Invoke-TestCase } 'Reasonix is running'
  Assert-Equal $script:events.Count 0

  foreach ($version in @('v1.38.9', 'v1.38.9-2', 'v1.38.9-preview.42', 'v1.38.9-rc.1')) {
    New-TestCase $version
    Invoke-TestCase
    $expected = if ($version.Contains('-')) { 'install,fresh-install,install,repaired-install,uninstall' } else { 'install,fresh-install,uninstall' }
    Assert-Equal ($script:events -join ',') $expected
    $report = Get-Content -LiteralPath (Join-Path $script:evidence 'acceptance.json') -Raw | ConvertFrom-Json
    Assert-Equal $report.artifact (Get-FileHash -Algorithm SHA256 -LiteralPath $script:artifact).Hash
    Assert-Equal $report.freshInstall 'passed'
    Assert-Equal $report.repairedInstall $(if ($version.Contains('-')) { 'passed' } else { 'not applicable' })
    Assert-Equal $report.uninstall 'passed'
    Assert-Equal $report.dataPreserved 'passed'
    Assert-ParentEnvironment
  }
  foreach ($phase in @('fresh-install', 'repaired-install')) {
    New-TestCase
    $script:failPhase = $phase
    Assert-Throws { Invoke-TestCase } "runtime failed: $phase"
    $expected = if ($phase -eq 'fresh-install') { 'install,fresh-install' } else { 'install,fresh-install,install,repaired-install' }
    Assert-Equal ($script:events -join ',') $expected
    Assert-Equal (Test-Path -LiteralPath (Join-Path $script:evidence 'acceptance.json')) $false
    Assert-ParentEnvironment
  }
  foreach ($fault in @('noop','program','shortcut','registration','process','default-data','runtime-data','modified-data')) {
    New-TestCase
    $script:uninstallFault = $fault
    Assert-Throws { Invoke-TestCase } 'Uninstall '
    Assert-Equal ($script:events -join ',') 'install,fresh-install,install,repaired-install,uninstall'
    Assert-Equal (Test-Path -LiteralPath (Join-Path $script:evidence 'acceptance.json')) $false
    Assert-ParentEnvironment
  }

  # Exercise the actual runtime entry point in both modes, stopping at its
  # first process boundary. This verifies shared isolation wiring and failure
  # restoration without launching an Electron instance.
  foreach ($mode in @('Installed', 'Portable')) {
    New-TestCase
    $fixture = Join-Path $script:testRoot "runtime-$script:testCount"
    $null = [IO.Directory]::CreateDirectory($fixture)
    [IO.File]::WriteAllText((Join-Path $fixture 'current.json'), '{"activeVersion":"v1.2.3","activeDir":"versions/v1.2.3"}')
    [IO.File]::WriteAllText((Join-Path $fixture 'Reasonix.exe'), 'never executed')
    $parameters = @{ EvidenceDirectory = $script:evidence }
    if ($mode -eq 'Installed') {
      $parameters.InstallRoot = $fixture
    } else {
      $zip = Join-Path $script:testRoot "portable-$script:testCount.zip"
      Compress-Archive -Path (Join-Path $fixture '*') -DestinationPath $zip
      $parameters.PortableZip = $zip
    }
    Assert-Throws {
      $runtimeProbeHome = Join-Path $script:evidence 'home'
      function Start-Process {
        param([string]$FilePath)
        Assert-IsolatedEnvironment $runtimeProbeHome
        throw 'Runtime environment probe complete'
      }
      & (Join-Path $PSScriptRoot 'test-windows-startup-recovery.ps1') @parameters
    } 'Runtime environment probe complete'
    Assert-ParentEnvironment
  }

  # Nested scopes restore the outer isolated home, then the original caller.
  New-TestCase
  $outerHome = Join-Path $script:testRoot 'unused\..\outer-home'
  $outerEnvironment = Enter-WindowsAcceptanceEnvironment -DataHome $outerHome
  try {
    $innerEnvironment = Enter-WindowsAcceptanceEnvironment -DataHome (Join-Path $script:testRoot 'inner-home')
    try { Assert-IsolatedEnvironment (Join-Path $script:testRoot 'inner-home') }
    finally { Restore-WindowsAcceptanceEnvironment -Snapshot $innerEnvironment }
    Assert-IsolatedEnvironment $outerHome
    $env:REASONIX_CREATED_INSIDE_TEST = 'remove on exit'
  } finally { Restore-WindowsAcceptanceEnvironment -Snapshot $outerEnvironment }
  Assert-ParentEnvironment
  Assert-Equal ([string]$env:REASONIX_CREATED_INSIDE_TEST) ''
  New-TestCase 'v1.38.9'
  $script:changeArtifact = $true
  Assert-Throws { Invoke-TestCase } 'Installer bytes changed'
  Assert-Equal (Test-Path -LiteralPath (Join-Path $script:evidence 'acceptance.json')) $false

  New-TestCase
  $env:GITHUB_ACTIONS = 'true'
  $env:RUNNER_ENVIRONMENT = 'github-hosted'
  Assert-InstallerTestAccount
  $script:occupiedPath = $protected[0]
  Assert-Throws { Assert-InstallerTestAccount } 'Existing Reasonix'
  Write-Host "PASS: $script:testCount installer acceptance cases (mock processes and registry; no real installation)."
} finally {
  Restore-WindowsAcceptanceEnvironment -Snapshot $saved
  [IO.Directory]::Delete($script:testRoot, $true)
}
