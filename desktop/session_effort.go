package main

import "reasonix/internal/config"

// rebindEffortModel commits the model/effort pair under the caller's tab lock.
func (t *WorkspaceTab) rebindEffortModel(cfg *config.Config, model string) {
	t.effort = config.RebindSessionEffort(cfg, t.model, model, t.effort)
	t.model = model
}
