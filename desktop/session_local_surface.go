package main

// selectLocalSurfaceAfterOpen makes a bound local session the visible surface
// even when a remote tab was selected before the request. It serializes with
// direct tab clicks, so a newer selection is never cleared by this completion.
func (a *App) selectLocalSurfaceAfterOpen(navigationSequence uint64) {
	a.tabSelectionMu.Lock()
	defer a.tabSelectionMu.Unlock()
	if a.desktopSessions.navigationSeq.Load() != navigationSequence {
		return
	}
	a.remoteTabMu.Lock()
	a.remoteTabLayout.activeID = ""
	a.remoteTabMu.Unlock()
	a.queueCurrentTabLayout()
}
