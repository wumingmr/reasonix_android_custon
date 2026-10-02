package config

import (
	"strings"

	"reasonix/internal/extension/protocol"
)

// RemovePluginModelRefs moves every model ref owned by pluginID to the first
// remaining configured provider, or clears it when none is left, and reports
// whether anything changed. Plugin refs resolve outside the config catalog, so
// a removed plugin would otherwise strand them and fail the next boot.
func (c *Config) RemovePluginModelRefs(pluginID string) bool {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return false
	}
	ownedByPlugin := func(ref string) bool {
		return protocol.PluginRefOwner(strings.TrimSpace(ref)) == pluginID
	}

	defaultOwned := ownedByPlugin(c.DefaultModel)
	plannerOwned := ownedByPlugin(c.Agent.PlannerModel)
	subagentOwned := ownedByPlugin(c.Agent.SubagentModel)
	subagentModelsOwned := map[string]bool{}
	for skill, ref := range c.Agent.SubagentModels {
		if ownedByPlugin(ref) {
			subagentModelsOwned[skill] = true
		}
	}
	if !defaultOwned && !plannerOwned && !subagentOwned && len(subagentModelsOwned) == 0 {
		return false
	}

	fallback := c.providerRemovalFallback("")
	if defaultOwned {
		c.DefaultModel = fallback
	}
	if plannerOwned {
		c.Agent.PlannerModel = fallback
	}
	if subagentOwned {
		c.Agent.SubagentModel = fallback
	}
	for skill := range subagentModelsOwned {
		if fallback != "" {
			c.Agent.SubagentModels[skill] = fallback
		} else {
			delete(c.Agent.SubagentModels, skill)
		}
	}
	return true
}
