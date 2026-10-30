package config

import (
	"errors"
	"strings"
)

// ErrModelContextWindowNegative refuses a per-model window below zero. Zero is
// how a model inherits its connection's window, so a negative value has no
// meaning to fall back to.
var ErrModelContextWindowNegative = errors.New("a model's context window cannot be negative")

// ModelLimits is what one model declares for itself on a connection whose
// models do not share the same budgets. Zero inherits; a negative output cap is
// the stored spelling of "send no cap" and is kept exactly as written.
type ModelLimits struct {
	ContextWindow   int
	MaxOutputTokens int
}

// ModelLimits reports the limits stored for model itself. The bool is false
// when the model declares none and inherits both, which is different from
// declaring zero-sized limits.
func (e *ProviderEntry) ModelLimits(model string) (ModelLimits, bool) {
	ov, ok := e.modelOverrideForModel(model)
	if !ok || (ov.ContextWindow <= 0 && ov.MaxOutputTokens == 0) {
		return ModelLimits{}, false
	}
	return ModelLimits{ContextWindow: max(ov.ContextWindow, 0), MaxOutputTokens: ov.MaxOutputTokens}, true
}

// SetModelLimits stores limits for model, or clears them when both are zero so
// the model inherits again. Every other field of that model's override — its
// reasoning protocol, effort ladder, vision flag — is preserved.
func (e *ProviderEntry) SetModelLimits(model string, limits ModelLimits) error {
	model = strings.TrimSpace(model)
	if e == nil || model == "" {
		return nil
	}
	if limits.ContextWindow < 0 {
		return ErrModelContextWindowNegative
	}
	key := e.storedModelOverrideKey(model)
	ov := e.ModelOverrides[key]
	ov.ContextWindow, ov.MaxOutputTokens = limits.ContextWindow, limits.MaxOutputTokens
	if modelOverrideEmpty(ov) {
		delete(e.ModelOverrides, key)
		if len(e.ModelOverrides) == 0 {
			e.ModelOverrides = nil
		}
		return nil
	}
	if e.ModelOverrides == nil {
		e.ModelOverrides = map[string]ProviderModelOverride{}
	}
	e.ModelOverrides[key] = ov
	return nil
}

// InheritedLimits is what model runs on when it declares no limits: the entry
// resolved onto that model with only those two fields of its override removed.
// Surfaces that show a blank field next to an inherited value use it, so a
// blank means the number displayed rather than an unknown.
func (e *ProviderEntry) InheritedLimits(model string) ModelLimits {
	if e == nil {
		return ModelLimits{}
	}
	cp := *e
	cp.Model = strings.TrimSpace(model)
	if key := cp.storedModelOverrideKey(cp.Model); key != "" {
		if _, ok := cp.ModelOverrides[key]; ok {
			overrides := make(map[string]ProviderModelOverride, len(cp.ModelOverrides))
			for k, v := range cp.ModelOverrides {
				overrides[k] = v
			}
			ov := overrides[key]
			ov.ContextWindow, ov.MaxOutputTokens = 0, 0
			overrides[key] = ov
			cp.ModelOverrides = overrides
		}
	}
	resolved := ResolveReasoningEntry(&cp)
	return ModelLimits{ContextWindow: resolved.ContextWindow, MaxOutputTokens: resolved.MaxOutputTokens}
}

// storedModelOverrideKey is the key a new or existing override for model is
// stored under: the existing key when one matches case-insensitively, so an
// edit never leaves the model declared twice, else the trimmed id itself.
func (e *ProviderEntry) storedModelOverrideKey(model string) string {
	model = strings.TrimSpace(model)
	if e == nil || model == "" {
		return model
	}
	if _, ok := e.ModelOverrides[model]; ok {
		return model
	}
	for key := range e.ModelOverrides {
		if strings.EqualFold(strings.TrimSpace(key), model) {
			return key
		}
	}
	return model
}
