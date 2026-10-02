package main

import (
	"log/slog"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

// archivedRuntimeCleanup owns post-commit joins. Keep removal/admission
// ownership until finish returns, but never hold title/index/App locks here:
// an in-flight autosave may need those locks before it can exit.
type archivedRuntimeCleanup struct {
	app     *App
	removed []removedSessionRuntime
	refs    []session.SessionRef
}

func (c *archivedRuntimeCleanup) finish() {
	a := c.app
	a.finalizeRemovedTopicRuntimes(c.removed)
	a.closeRemainingRemovedSessionRuntimesAdmissionHeld(c.removed, map[control.SessionAPI]bool{})
	for _, ref := range c.refs {
		if err := a.retireArchivedSessionRuntime(a.bootContext(), ref); err != nil {
			// Archive is already durable; purge can retry retirement later.
			slog.Warn("desktop: archived runtime retirement deferred", "err", err)
		}
	}
}
