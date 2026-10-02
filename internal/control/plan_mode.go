package control

import (
	"encoding/json"
	"log/slog"
)

func (c *Controller) applyPlanMode(v bool) {
	c.mu.Lock()
	changed := c.sessionSettings.planMode != v
	c.sessionSettings.planMode = v
	c.mu.Unlock()
	if setter, ok := c.runner.(interface{ SetPlanMode(bool) }); ok {
		setter.SetPlanMode(v)
	} else if c.executor != nil {
		c.executor.SetPlanMode(v)
	}
	if !changed {
		return
	}
	payload, _ := json.Marshal(map[string]any{"enabled": v})
	if err := c.appendDomainState("plan/state", payload, "mode"); err != nil {
		slog.Warn("controller: append plan mode event", "err", err)
	}
}
