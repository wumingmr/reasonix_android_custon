package main

import "strings"

func prepareRestoredTabIdentity(tab *WorkspaceTab, entry desktopTabEntry) bool {
	tab.goal = strings.TrimSpace(entry.Goal)
	tab.SessionHeadID = entry.SessionHeadID
	if entry.historicalSource != nil {
		// Only the current single surface reaches this function during startup.
		// Preserve its native identity; no import is needed to restore execution.
		tab.SessionPath = entry.historicalSource.Path
		tab.SessionHeadID = entry.historicalSource.HeadID
		tab.retainLegacyPinnedFiles(entry.PinnedFiles)
		return true
	}
	if entry.restoreBlocked {
		tab.StartupErr = "Saved session identity could not be verified. Recovery data was preserved."
		tab.retainLegacyPinnedFiles(entry.PinnedFiles)
		return false
	}
	tab.goal = runningTabSessionGoal(strings.TrimSpace(entry.SessionPath), tab.goal)
	restoreTabPinnedContext(tab, entry.PinnedFiles)
	return true
}
