package main

import (
	"log/slog"
	"os"

	"reasonix/internal/boot"
	"reasonix/internal/skill/skillwatch"
)

// sharedSkillWatchService returns the one skill-watch service this host shares
// across every controller it builds.
//
// Each service owns a helper process: a service per build spawned a 56MB
// reasonix-desktop.exe and tore one down on every tab-controller rebuild, and
// because a helper lives until its build is closed, rebuilds left several
// resident at once. One service per host keeps exactly one helper for the
// process lifetime.
func (a *App) sharedSkillWatchService() *skillwatch.Service {
	if a == nil {
		return nil
	}
	a.skillWatchMu.Lock()
	defer a.skillWatchMu.Unlock()
	if a.skillWatch == nil {
		a.skillWatch = boot.NewHostSkillWatchService(os.Stderr)
	}
	return a.skillWatch
}

// closeSharedSkillWatchService closes the host watcher; boot.Build leaves a
// caller-owned service alone, so nothing else closes it. The closed service
// stays in place: a build racing shutdown gets dead subscriptions from it
// instead of a fresh helper process that nothing would close.
func (a *App) closeSharedSkillWatchService() {
	if a == nil {
		return
	}
	a.skillWatchMu.Lock()
	service := a.skillWatch
	a.skillWatchMu.Unlock()
	if service == nil {
		return
	}
	if err := service.Close(); err != nil {
		slog.Warn("desktop: close skill watch service", "err", err)
	}
}
