# The selector is deliberately scoped to the rendered transcript. Sidebar topic
# titles and offscreen accessibility nodes are not evidence of restored history.
function Get-UpgradeUIDescendants($element) {
  return $element.FindAll([Windows.Automation.TreeScope]::Descendants, [Windows.Automation.Condition]::TrueCondition)
}

function Invoke-UpgradeUIButton($element) {
  $pattern = $element.GetCurrentPattern([Windows.Automation.InvokePattern]::Pattern)
  $pattern.Invoke()
}

function Invoke-PendingHistoricalSession($root) {
  foreach ($element in (Get-UpgradeUIDescendants $root)) {
    $current = $element.Current
    if ($current.AutomationId -eq 'reasonix-prepare-restored-session' -and -not $current.IsOffscreen -and
        $current.BoundingRectangle.Width -gt 0 -and $current.BoundingRectangle.Height -gt 0) {
      Invoke-UpgradeUIButton $element
      return $true
    }
  }
  return $false
}

function Get-UpgradeUITimeMilliseconds {
  return [long]([Diagnostics.Stopwatch]::GetTimestamp() * 1000.0 / [Diagnostics.Stopwatch]::Frequency)
}

function Wait-UpgradeUIPoll { Start-Sleep -Milliseconds 250 }

# This is functional recovery acceptance, not a cold-import latency SLO. A
# loaded Windows runner can spend the old entire 20-second budget importing,
# activating and exposing the first Markdown body through UI Automation. Keep
# a bounded completion wait and record its cost; startup readiness has its own
# separate deadline. Always sample before checking the deadline, including the
# final observation, instead of failing on a previously sampled empty surface.
function Wait-VisibleUpgradeHistory {
  param(
    [Parameter(Mandatory=$true)][scriptblock]$ReadRoot,
    [Parameter(Mandatory=$true)][string]$Text,
    [switch]$PrepareHistoricalSession,
    [ValidateRange(1,300)][int]$TimeoutSeconds = 60
  )
  $started = Get-UpgradeUITimeMilliseconds
  $prepared = -not $PrepareHistoricalSession
  $found = $false
  $root = $null
  $polls = 0
  while ($true) {
    $root = & $ReadRoot
    $polls++
    if ($null -ne $root) {
      if (-not $prepared) { $prepared = Invoke-PendingHistoricalSession $root }
      # Lazy history is readable without activating a runtime. The import
      # action blocks acceptance only when this wait was asked to prepare;
      # otherwise it is the banner every unprepared legacy tab carries.
      $found = Test-VisibleUpgradeHistory $root $Text -AllowPendingSource:(-not $PrepareHistoricalSession)
      if ($found) { $prepared = $true }
    }
    $elapsed = (Get-UpgradeUITimeMilliseconds) - $started
    if ($found -or $elapsed -ge ($TimeoutSeconds * 1000)) { break }
    Wait-UpgradeUIPoll
  }
  return [pscustomobject]@{Root=$root; Found=$found; Prepared=$prepared; ElapsedMilliseconds=$elapsed; Polls=$polls; TimeoutSeconds=$TimeoutSeconds}
}

function Test-VisibleUpgradeHistory($root, [string]$text, [switch]$AllowPendingSource) {
  if ([string]::IsNullOrWhiteSpace($text)) { return $false }
  $descendants = @(Get-UpgradeUIDescendants $root)
  foreach ($element in $descendants) {
    $current = $element.Current
    if ($current.IsOffscreen -or $current.BoundingRectangle.Width -le 0 -or $current.BoundingRectangle.Height -le 0) { continue }
    # Old content alone cannot prove recovery: reject a still-loading/failed
    # recovery surface and the obsolete chat notice that originally hid this bug.
    if (([string]$current.AutomationId).StartsWith('reasonix-session-recovery-') -or
        (-not $AllowPendingSource -and $current.AutomationId -eq 'reasonix-prepare-restored-session') -or
        ([string]$current.Name).Contains('Failed to load conversation history.') -or
        ([string]$current.Name).Contains('加载会话历史失败。') -or
        ([string]$current.Name).Contains('載入會話歷史失敗。')) { return $false }
  }
  foreach ($transcript in $descendants) {
    if (-not ([string]$transcript.Current.AutomationId).StartsWith('reasonix-chat-transcript-') -or $transcript.Current.IsOffscreen) { continue }
    foreach ($element in (Get-UpgradeUIDescendants $transcript)) {
      $current = $element.Current
      if (-not $current.IsOffscreen -and $current.BoundingRectangle.Width -gt 0 -and $current.BoundingRectangle.Height -gt 0 -and
          $current.Name.Contains($text)) { return $true }
    }
  }
  return $false
}
