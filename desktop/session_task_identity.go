package main

import (
	"reasonix/internal/agent"
	"reasonix/internal/control"
)

// controllerTaskSessionID never derives a canonical identity from its empty
// compatibility path. Legacy controllers keep their persisted branch IDs.
func controllerTaskSessionID(ctrl control.SessionAPI) string {
	if ctrl == nil {
		return ""
	}
	if lifecycle, ok := ctrl.(control.IdentityLifecycle); ok && lifecycle.UsesExclusiveSession() {
		if ref, bound := lifecycle.SessionRef(); bound {
			return ref.SessionID
		}
		return ""
	}
	return agent.BranchID(ctrl.SessionPath())
}
