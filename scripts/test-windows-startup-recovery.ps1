[CmdletBinding(DefaultParameterSetName='Portable')]
param(
  [Parameter(Mandatory=$true, ParameterSetName='Portable')][string]$PortableZip,
  [Parameter(Mandatory=$true, ParameterSetName='Installed')][string]$InstallRoot,
  [string]$ExpectedVersion = '',
  [string]$ArtifactPath = '',
  [string]$DataHome = '',
  [string]$ExpectedVisibleText = '',
  [switch]$PrepareHistoricalSession,
  [string]$EvidenceDirectory = (Join-Path $env:TEMP ('reasonix-recovery-' + [guid]::NewGuid().ToString('N')))
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'windows-acceptance-environment.ps1')
. (Join-Path $PSScriptRoot 'windows-upgrade-ui-evidence.ps1')
New-Item -ItemType Directory -Force $EvidenceDirectory | Out-Null
$install = if ($PSCmdlet.ParameterSetName -eq 'Installed') { [IO.Path]::GetFullPath($InstallRoot) } else { Join-Path $EvidenceDirectory 'install' }
$dataHome = if ($DataHome) { [IO.Path]::GetFullPath($DataHome) } else { Join-Path $EvidenceDirectory 'home' }
if ($PSCmdlet.ParameterSetName -eq 'Portable') {
  if (Test-Path $install) { throw 'Evidence install directory must be new; refusing to overwrite a running fixture.' }
  Expand-Archive -LiteralPath $PortableZip -DestinationPath $install
} elseif (-not (Test-Path -LiteralPath $install -PathType Container)) {
  throw "Installed Reasonix directory is missing: $install"
}
$current = Get-Content (Join-Path $install 'current.json') -Raw | ConvertFrom-Json
if ($ExpectedVersion -and $current.activeVersion -ne $ExpectedVersion) {
  throw "Installed version mismatch: current.json=$($current.activeVersion), expected=$ExpectedVersion"
}
$shellPath = Join-Path $install ($current.activeDir + '\app\Reasonix.exe')
$launcher = Join-Path $install 'Reasonix.exe'
if (-not (Test-Path -LiteralPath $launcher -PathType Leaf)) {
  $launcher = Join-Path $install 'reasonix-launcher.exe'
}
if (-not (Test-Path -LiteralPath $launcher -PathType Leaf)) {
  throw 'Stable Reasonix launcher is missing from the package root.'
}

function Read-ShellStatus($process) {
  $pipe = [IO.Pipes.NamedPipeClientStream]::new('.', ('reasonix-shell-v1-' + $process.Id), [IO.Pipes.PipeDirection]::In)
  try {
    $pipe.Connect(500)
    $reader = [IO.StreamReader]::new($pipe)
    $read = $reader.ReadLineAsync()
    if (-not $read.Wait(2000)) { return $null }
    if ($read.Result.Length -gt 16384) { throw 'Oversized status response' }
    return ($read.Result | ConvertFrom-Json)
  } catch { return $null } finally { $pipe.Dispose() }
}

function Assert-Ready {
  foreach ($process in @(Get-Process Reasonix -ErrorAction SilentlyContinue)) {
    if ($process.Path -ne $shellPath) { continue }
    $status = Read-ShellStatus $process
    if ($null -eq $status) { continue }
    if ($status.schemaVersion -ne 1 -or $status.product -ne 'com.reasonix.desktop' -or $status.pid -ne $process.Id) { throw 'Status identity mismatch' }
    if ($status.lifecycle -ne 'ready' -or $status.service -ne 'ready' -or -not $status.visible -or -not $status.healthy) { throw ('Not ready: ' + ($status | ConvertTo-Json -Compress)) }
    if ($status.version -ne $current.activeVersion -or $status.rendererVersion -ne $current.activeVersion) { throw 'Target renderer version mismatch' }
    $service = Get-Process -Id $status.servicePID
    $expectedService = Join-Path $install ($current.activeDir + '\reasonix-desktop.exe')
    if ($service.Path -ne $expectedService) { throw 'Service is outside the active release' }
    return @{ Shell=$process; Service=$service; Status=$status }
  }
  throw 'No verified target shell; inspect preserved logs'
}

function Assert-VisibleContentAndCapture($process, [string]$text, [string]$outputPath) {
  Add-Type -AssemblyName UIAutomationClient
  Add-Type -AssemblyName System.Drawing
  $observation = Wait-VisibleUpgradeHistory -Text $text -PrepareHistoricalSession:$PrepareHistoricalSession -ReadRoot {
    $process.Refresh()
    if ($process.MainWindowHandle -ne 0) {
      return [Windows.Automation.AutomationElement]::FromHandle($process.MainWindowHandle)
    }
  }
  $root = $observation.Root
  $found = $observation.Found
  $observation | Select-Object Found, Prepared, ElapsedMilliseconds, Polls, TimeoutSeconds |
    ConvertTo-Json | Set-Content -LiteralPath ($outputPath + '.wait.json') -Encoding utf8
  if ($null -eq $root) { throw 'The packaged shell did not expose a native window for upgrade evidence.' }
  @(Get-UpgradeUIDescendants $root | Select-Object -First 5000 | ForEach-Object {
    @{name=$_.Current.Name; id=$_.Current.AutomationId; offscreen=$_.Current.IsOffscreen}
  }) | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath ($outputPath + '.uia.json') -Encoding utf8
  $bounds = $root.Current.BoundingRectangle
  if ($bounds.Width -le 0 -or $bounds.Height -le 0) { throw 'The packaged shell window has invalid bounds.' }
  $bitmap = [Drawing.Bitmap]::new([int]$bounds.Width, [int]$bounds.Height)
  try {
    $graphics = [Drawing.Graphics]::FromImage($bitmap)
    try { $graphics.CopyFromScreen([int]$bounds.X, [int]$bounds.Y, 0, 0, $bitmap.Size) } finally { $graphics.Dispose() }
    $bitmap.Save($outputPath, [Drawing.Imaging.ImageFormat]::Png)
  } finally { $bitmap.Dispose() }
  if (-not $found) { throw "Packaged UI did not expose expected upgraded history text: $text" }
}

$acceptanceEnvironment = Enter-WindowsAcceptanceEnvironment -DataHome $dataHome
try {
  $attempt = Start-Process $launcher -PassThru -RedirectStandardOutput (Join-Path $EvidenceDirectory 'launcher.stdout.log') -RedirectStandardError (Join-Path $EvidenceDirectory 'launcher.stderr.log')
  $null = $attempt.Handle
  if (-not $attempt.WaitForExit(40000)) { throw 'Stable launcher timed out; inspect launcher logs' }
  if ($attempt.ExitCode -ne 0) { throw ('Stable launcher failed: exit=' + $attempt.ExitCode + '; ' + (Get-Content (Join-Path $EvidenceDirectory 'launcher.stderr.log') -Raw)) }
  $first = Assert-Ready
  $again = Start-Process $launcher -PassThru
  $null = $again.Handle
  if (-not $again.WaitForExit(40000) -or $again.ExitCode -ne 0) { throw 'Second launch failed or timed out' }
  $second = Assert-Ready
  if ($first.Shell.Id -ne $second.Shell.Id -or $first.Service.Id -ne $second.Service.Id -or $first.Status.generation -ne $second.Status.generation) { throw 'Second launch replaced the healthy instance' }
  if ($ExpectedVisibleText) {
    Assert-VisibleContentAndCapture $first.Shell $ExpectedVisibleText (Join-Path $EvidenceDirectory 'upgraded-history.png')
  }
  Start-Process $shellPath -ArgumentList '--reasonix-lifecycle-request=quit' | Out-Null
  if (-not $first.Shell.WaitForExit(15000) -or -not $first.Service.WaitForExit(1000)) { throw 'Normal exit left a shell or service alive' }
  $shellLog = Get-Content (Join-Path $dataHome 'desktop-shell\logs\shell.log') -Raw
  if ($shellLog -match 'exit deadline exceeded|killing it|termination failed') { throw 'Forced cleanup is not a normal-exit pass' }
  $artifactHash = if ($ArtifactPath) { (Get-FileHash -Algorithm SHA256 $ArtifactPath).Hash } elseif ($PortableZip) { (Get-FileHash -Algorithm SHA256 $PortableZip).Hash } else { '' }
  $report = @{
    artifact=$artifactHash
    version=$current.activeVersion
    OS=[Environment]::OSVersion.VersionString
    architecture=$env:PROCESSOR_ARCHITECTURE
    shellPID=$first.Shell.Id
    servicePID=$first.Service.Id
    startup='passed'; secondLaunch='passed'; normalExit='passed'; visibleHistory=if ($ExpectedVisibleText) { 'passed' } else { 'not requested' }
    signing='not checked'; installer=if ($PSCmdlet.ParameterSetName -eq 'Installed') { 'passed' } else { 'not exercised' }; recoveryConsent='not exercised'
  }
  $report | ConvertTo-Json | Set-Content (Join-Path $EvidenceDirectory 'result.json')
  $report | ConvertTo-Json
} finally {
  Restore-WindowsAcceptanceEnvironment -Snapshot $acceptanceEnvironment
  Write-Host "Evidence preserved at $EvidenceDirectory. No forced process cleanup was performed."
}
