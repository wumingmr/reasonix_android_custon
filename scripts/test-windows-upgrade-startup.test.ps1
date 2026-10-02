$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'test-windows-upgrade-startup.ps1')
. (Join-Path $PSScriptRoot 'windows-upgrade-ui-evidence.ps1')

function Assert-True($value, [string]$message) { if (-not $value) { throw $message } }
function Assert-Throws([scriptblock]$action, [string]$message) {
  $caught = $false
  try { & $action } catch { $caught = $true }
  Assert-True $caught $message
}

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('reasonix-upgrade-contract-' + [guid]::NewGuid().ToString('N'))
$savedHome = $HOME
try {
  $release = Join-Path $testRoot 'install/releases/test/app/resources'
  New-Item -ItemType Directory -Path $release -Force | Out-Null
  $application = Join-Path $testRoot 'install/Reasonix.exe'
  $builder = Join-Path $testRoot 'fixture.exe'
  Set-Content -LiteralPath $application -Value 'mock launcher'
  Set-Content -LiteralPath $builder -Value 'mock builder'
  $version = 'v1.38.9-5'
  $commit = '0123456789abcdef0123456789abcdef01234567'
  @{activeVersion=$version; activeDir='releases/test'} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $testRoot 'install/current.json')
  $buildPath = Join-Path $release 'build.json'
  @{version=$version; commit=$commit} | ConvertTo-Json | Set-Content -LiteralPath $buildPath
  $script:calls = [Collections.Generic.List[string]]::new()
  $script:failPhase = ''
  # Only replace native process boundaries; run the real evidence orchestration.
  function Invoke-UpgradeFixture([string]$builder, [string[]]$arguments) {
    $mode = $arguments[1]
    $fixtureHome = $arguments[3]
    $report = $arguments[5]
    Assert-True ($fixtureHome -ne $HOME -and $fixtureHome.Contains('home # %20 中文')) 'Fixture must use a disposable home.'
    if ($mode -eq 'create') {
      $script:calls.Add('create')
      @{visibleText='assistant-only marker'} | ConvertTo-Json | Set-Content -LiteralPath $report
    } else {
      $phase = $arguments[7]
      $script:calls.Add("verify-$phase")
      if ($script:failPhase -eq $phase) { throw 'Injected verification failure' }
    }
  }
  function Invoke-UpgradeStartup([string]$installRoot, [string]$version, [string]$fixtureHome, [string]$text, [string]$evidence, [bool]$prepareHistorical) {
    Assert-True ($text -eq 'assistant-only marker') 'Startup must require the assistant marker.'
    Assert-True ($prepareHistorical -eq ((Split-Path $evidence -Leaf) -eq 'restart')) 'The first launch must show legacy history unprepared; only the restart may import it.'
    $script:calls.Add('startup-' + (Split-Path $evidence -Leaf))
  }
  $evidence = Join-Path $testRoot 'success'
  Invoke-WindowsUpgradeAcceptance $application $builder $version $evidence
  $result = Get-Content -LiteralPath (Join-Path $evidence 'result.json') -Raw | ConvertFrom-Json
  Assert-True ($HOME -eq $savedHome) 'Acceptance changed the PowerShell home.'
  Assert-True ($result.sourceSha -eq $commit) 'Evidence must read build.json.commit.'
  Assert-True ($result.applicationSha256 -eq (Get-FileHash -Algorithm SHA256 -LiteralPath $application).Hash) 'Evidence must hash the tested application.'
  Assert-True (($script:calls -join ',') -eq 'create,startup-first,verify-first,startup-restart,verify-restart') 'Upgrade and restart must both be checked in order.'

  foreach ($invalidCommit in @('', 'unknown', '0123456')) {
    @{version=$version; commit=$invalidCommit; sourceSha=$commit} | ConvertTo-Json | Set-Content -LiteralPath $buildPath
    $badEvidence = Join-Path $testRoot ('invalid-' + [guid]::NewGuid().ToString('N'))
    Assert-Throws { Invoke-WindowsUpgradeAcceptance $application $builder $version $badEvidence } 'Missing/invalid commit must fail closed.'
    Assert-True (-not (Test-Path -LiteralPath $badEvidence)) 'Invalid package must not get acceptance evidence.'
  }
  @{version=$version; commit=$commit} | ConvertTo-Json | Set-Content -LiteralPath $buildPath
  foreach ($phase in @('first', 'restart')) {
    $script:failPhase = $phase
    $badEvidence = Join-Path $testRoot "failure-$phase"
    Assert-Throws { Invoke-WindowsUpgradeAcceptance $application $builder $version $badEvidence } 'Data verification failure must propagate.'
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $badEvidence 'result.json'))) 'Failed verification cannot publish a pass.'
  }

  # Model the native tree enumeration boundary. Selection and visibility checks
  # below use the same function as packaged Windows acceptance.
  function Get-UpgradeUIDescendants($element) {
    foreach ($child in $element.Children) { $child; Get-UpgradeUIDescendants $child }
  }
  function New-UIElement([string]$name, [string]$id='', [bool]$offscreen=$false, [object[]]$children=@()) {
    return [pscustomobject]@{
      Current=[pscustomobject]@{Name=$name; AutomationId=$id; IsOffscreen=$offscreen; BoundingRectangle=[pscustomobject]@{Width=200; Height=20}}
      Children=$children
    }
  }
  $marker = 'assistant-only marker'
  $script:clicked = 0
  function Invoke-UpgradeUIButton($element) { $script:clicked++ }
  $pending = New-UIElement 'Import and open' 'reasonix-prepare-restored-session'
  $pendingRoot = New-UIElement '' '' $false @($pending)
  $pending.Current.IsOffscreen = $true
  Assert-True (-not (Invoke-PendingHistoricalSession $pendingRoot)) 'Hidden preparation actions must not be clicked.'
  $pending.Current.IsOffscreen = $false
  Assert-True (Invoke-PendingHistoricalSession $pendingRoot) 'Explicit preparation must invoke the actual pending-session action.'
  Assert-True ($script:clicked -eq 1) 'Invoke only the visible action.'
  $body = New-UIElement $marker
  $transcript = New-UIElement '' 'reasonix-chat-transcript-upgrade-tab' $false @($body)
  $sidebar = New-UIElement $marker 'sidebar-topic'
  $root = New-UIElement '' '' $false @($sidebar)
  Assert-True (-not (Test-VisibleUpgradeHistory $root $marker)) 'A matching sidebar title must not pass.'
  $root.Children = @($sidebar, $transcript)
  Assert-True (Test-VisibleUpgradeHistory $root $marker) 'Visible transcript assistant body should pass.'
  $root.Children = @($sidebar, $transcript, $pending)
  Assert-True (-not (Test-VisibleUpgradeHistory $root $marker)) 'Prepared content must not retain the import action.'
  Assert-True (Test-VisibleUpgradeHistory $root $marker -AllowPendingSource) 'Unprepared legacy history may show the import action beside its body.'
  $root.Children = @($sidebar, $pending)
  Assert-True (-not (Test-VisibleUpgradeHistory $root $marker -AllowPendingSource)) 'The import action alone is not visible history.'
  $root.Children = @($sidebar, $transcript)
  $failure = New-UIElement 'Failed to load conversation history. Previous content was kept when available — retry to try again.'
  $transcript.Children = @($body, $failure)
  Assert-True (-not (Test-VisibleUpgradeHistory $root $marker)) 'Visible history with an obsolete failure notice must not pass.'
  $transcript.Children = @($body)
  $recovery = New-UIElement 'Loading history' 'reasonix-session-recovery-test'
  $root.Children = @($sidebar, $transcript, $recovery)
  Assert-True (-not (Test-VisibleUpgradeHistory $root $marker)) 'History behind an unresolved recovery banner must not pass.'
  $recovery.Current.IsOffscreen = $true
  Assert-True (Test-VisibleUpgradeHistory $root $marker) 'An offscreen recovery surface does not block the visible session.'
  $root.Children = @($sidebar, $transcript)
  $body.Current.IsOffscreen = $true
  Assert-True (-not (Test-VisibleUpgradeHistory $root $marker)) 'Offscreen history must not pass.'
  $body.Current.IsOffscreen = $false
  $transcript.Current.IsOffscreen = $true
  Assert-True (-not (Test-VisibleUpgradeHistory $root $marker)) 'Hidden transcript must not pass.'
  $transcript.Current.IsOffscreen = $false
  $body.Current.BoundingRectangle.Width = 0
  Assert-True (-not (Test-VisibleUpgradeHistory $root $marker)) 'Zero-size history must not pass.'
  $body.Current.BoundingRectangle.Width = 200
  $body.Current.Name = 'unrelated body'
  Assert-True (-not (Test-VisibleUpgradeHistory $root $marker)) 'Matching text outside the transcript must not pass.'

  # Exercise the real wait owner with a virtual monotonic clock. Slow import
  # and lazy body exposure are allowed without slowing this contract test.
  $script:upgradeWaitNow = 0
  function Get-UpgradeUITimeMilliseconds { return $script:upgradeWaitNow }
  function Wait-UpgradeUIPoll { $script:upgradeWaitNow += 250 }
  $script:upgradeWaitRoot = $root
  $slowBody = {
    $script:upgradeWaitRoot.Children = if ($script:clicked -eq 0) { @($pending) } else { @($transcript) }
    $body.Current.Name = if ($script:upgradeWaitNow -ge 30000) { $marker } else { 'body pending' }
    return $script:upgradeWaitRoot
  }
  $script:clicked = 0
  $oldBudget = Wait-VisibleUpgradeHistory -ReadRoot $slowBody -Text $marker -PrepareHistoricalSession -TimeoutSeconds 20
  Assert-True (-not $oldBudget.Found) 'The old fixed budget rejects a correct slow import.'
  $script:upgradeWaitNow = 0
  $script:clicked = 0
  $observed = Wait-VisibleUpgradeHistory -ReadRoot $slowBody -Text $marker -PrepareHistoricalSession
  Assert-True ($observed.Found -and $observed.ElapsedMilliseconds -eq 30000) 'Functional acceptance must observe the completed slow import.'
  Assert-True ($script:clicked -eq 1) 'Polling must never execute import twice.'

  $script:upgradeWaitNow = 0
  $atDeadline = Wait-VisibleUpgradeHistory -Text $marker -TimeoutSeconds 20 -ReadRoot {
    $script:upgradeWaitRoot.Children = @($transcript)
    $body.Current.Name = if ($script:upgradeWaitNow -ge 20000) { $marker } else { 'body pending' }
    return $script:upgradeWaitRoot
  }
  Assert-True ($atDeadline.Found -and $atDeadline.ElapsedMilliseconds -eq 20000) 'The deadline must use a final fresh observation, not the previous empty sample.'

  $script:upgradeWaitNow = 0
  $body.Current.Name = 'never restored'
  $missing = Wait-VisibleUpgradeHistory -ReadRoot { $script:upgradeWaitRoot } -Text $marker -TimeoutSeconds 1
  Assert-True (-not $missing.Found -and $missing.ElapsedMilliseconds -eq 1000) 'Missing history still fails within the bounded wait.'
  $script:upgradeWaitNow = 0
  $script:clicked = 0
  $body.Current.Name = $marker
  $script:upgradeWaitRoot.Children = @($transcript)
  $alreadyReadable = Wait-VisibleUpgradeHistory -ReadRoot { $script:upgradeWaitRoot } -Text $marker -PrepareHistoricalSession -TimeoutSeconds 1
  Assert-True ($alreadyReadable.Found -and $alreadyReadable.Prepared -and $script:clicked -eq 0) 'Readable history must pass without forcing runtime activation.'
  $script:upgradeWaitRoot.Children = @($transcript, $pending)
  $unprepared = Wait-VisibleUpgradeHistory -ReadRoot { $script:upgradeWaitRoot } -Text $marker -PrepareHistoricalSession -TimeoutSeconds 1
  Assert-True (-not $unprepared.Found) 'A matching body cannot bypass a still-pending preparation action.'
  $script:clicked = 0
  $legacyView = Wait-VisibleUpgradeHistory -ReadRoot { $script:upgradeWaitRoot } -Text $marker -TimeoutSeconds 1
  Assert-True ($legacyView.Found -and $script:clicked -eq 0) 'Without preparation the legacy body passes beside the import action and nothing is imported.'
  Write-Host 'Windows upgrade orchestration and UI evidence contracts passed (mocked native boundaries).'
} finally {
  Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
}
