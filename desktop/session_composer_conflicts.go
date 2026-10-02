package main

import "reasonix/internal/session"

// Retained for older renderers; formal input no longer exposes recovery copies.
func (a *App) ListSessionComposerConflicts(ref session.SessionRef) ([]string, error) {
	return []string{}, validateLocalSessionRef(ref)
}
