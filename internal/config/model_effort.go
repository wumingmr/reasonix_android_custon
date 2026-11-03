package config

import "fmt"

// SetRuntimeEffort keeps session/role overrides above persisted model settings.
func (e *ProviderEntry) SetRuntimeEffort(effort string) {
	e.Effort = effort
	e.effortRuntime = true
}

// SetModelEffort changes only this model. Nil restores connection inheritance;
// an explicit empty string selects auto, even if the connection pins an effort.
func (c *Config) SetModelEffort(name, model string, effort *string) error {
	entry, ok := c.Provider(name)
	selected, found := c.ResolveModel(name + "/" + model)
	if !ok || !found || selected.Name != name {
		return fmt.Errorf("unknown model %s/%s", name, model)
	}
	if effort != nil && *effort != "" {
		if _, err := NormalizeEffort(selected, *effort); err != nil {
			return err
		}
	}
	entry.ModelOverrides = cloneModelOverrideMap(entry.ModelOverrides)
	ov := entry.ModelOverrides[model]
	ov.Effort = nil
	if effort != nil {
		value := normalizeStoredEffort(*effort)
		ov.Effort = &value
	}
	if entry.ModelOverrides == nil {
		entry.ModelOverrides = map[string]ProviderModelOverride{}
	}
	if modelOverrideEmpty(ov) {
		delete(entry.ModelOverrides, model)
	} else {
		entry.ModelOverrides[model] = ov
	}
	return nil
}
