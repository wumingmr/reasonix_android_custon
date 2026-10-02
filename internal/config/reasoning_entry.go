package config

import (
	"slices"
	"strings"
)

// ResolveReasoningEntry overlays current built-in facts on a private model
// entry. Only the user's declarations live in Config. Missing declarations
// therefore follow catalog updates without rewriting a saved connection.
// Explicit model settings win over connection settings, then built-in facts.
func ResolveReasoningEntry(entry *ProviderEntry) *ProviderEntry {
	if entry == nil {
		return nil
	}
	e := *entry
	if e.Model == "" {
		e.Model = e.DefaultModel()
	}
	e.applyModelOverride()
	RepairProviderEndpointContract(&e)
	defaults, ok := builtinReasoningDefaults(&e)
	if !ok {
		return &e
	}
	protocol := explicitReasoningProtocol(&e)
	// A user-selected dialect owns its vocabulary. Never graft the preset's
	// DeepSeek levels onto an explicitly selected GLM/OpenAI/none protocol.
	if protocol != "" && protocol != defaults.ReasoningProtocol {
		return &e
	}
	if protocol == "" {
		e.ReasoningProtocol = defaults.ReasoningProtocol
		e.reasoningProtocolAutomatic = true
	}
	if len(e.SupportedEfforts) == 0 {
		e.SupportedEfforts = append([]string(nil), defaults.SupportedEfforts...)
		e.reasoningAutomatic = true
		if e.DefaultEffort == "" {
			e.DefaultEffort = defaults.DefaultEffort
			e.reasoningDefaultAutomatic = true
		}
	}
	return &e
}

// stripRuntimeReasoningDefaults prevents callers saving a resolved runtime
// entry (for example /effort materializing a default provider) from freezing
// inherited facts. Values changed since resolution remain explicit edits.
func stripRuntimeReasoningDefaults(e *ProviderEntry) {
	if !e.reasoningAutomatic && !e.reasoningProtocolAutomatic && !e.reasoningDefaultAutomatic {
		return
	}
	if defaults, ok := builtinReasoningDefaults(e); ok {
		if e.reasoningAutomatic && slices.Equal(e.SupportedEfforts, defaults.SupportedEfforts) {
			e.SupportedEfforts = nil
		}
		if e.reasoningProtocolAutomatic && e.ReasoningProtocol == defaults.ReasoningProtocol {
			e.ReasoningProtocol = ""
		}
		if e.reasoningDefaultAutomatic && e.DefaultEffort == defaults.DefaultEffort {
			e.DefaultEffort = ""
		}
	}
	e.reasoningAutomatic, e.reasoningProtocolAutomatic, e.reasoningDefaultAutomatic = false, false, false
}

// PresetReasoningForEdit removes generated reasoning metadata from a newly
// installed template. Existing saved declarations are never guessed to be
// generated: their provenance is unavailable, so they remain user-owned.
func presetReasoningForEdit(e *ProviderEntry) {
	e.ReasoningProtocol, e.DefaultEffort, e.SupportedEfforts = "", "", nil
	for model, ov := range e.ModelOverrides {
		ov.ReasoningProtocol, ov.DefaultEffort, ov.SupportedEfforts = "", "", nil
		if modelOverrideEmpty(ov) {
			delete(e.ModelOverrides, model)
		} else {
			e.ModelOverrides[model] = ov
		}
	}
	if len(e.ModelOverrides) == 0 {
		e.ModelOverrides = nil
	}
}

func builtinReasoningDefaults(e *ProviderEntry) (ProviderModelOverride, bool) {
	// Names and preset IDs are editable labels, not proof of endpoint identity.
	// Exact endpoint/protocol matching also keeps custom gateways and paths from
	// accidentally receiving a first-party contract.
	request, valid := normalizedExactProviderRequestURL(ProviderEffectiveRequestURL(e))
	if !valid {
		return ProviderModelOverride{}, false
	}
	for _, preset := range curatedProviderPresets {
		for _, template := range preset.Entries {
			if template.Kind != e.Kind || !acceptsDeepSeekModelReference(&template, e.Model) {
				continue
			}
			candidate, valid := normalizedExactProviderRequestURL(ProviderEffectiveRequestURL(&template))
			if !valid || candidate != request {
				continue
			}
			template.Model = e.Model
			template.applyModelOverride()
			return ProviderModelOverride{
				ReasoningProtocol: reasoningProtocolForResolvedEntry(&template),
				SupportedEfforts:  template.SupportedEfforts,
				DefaultEffort:     template.DefaultEffort,
			}, true
		}
	}
	return ProviderModelOverride{}, false
}

// RebindSessionEffort binds the existing model/effort pair as one selection.
// A route change clears inherited intent even when both models spell a level
// "high". Same-route values are preserved for strict validation, never clamped.
// Existing persisted model and effort fields remain the storage contract.
func RebindSessionEffort(cfg *Config, previous, next string, effort *string) *string {
	if effort == nil {
		return nil
	}
	canonical := func(ref string) string {
		ref = strings.TrimSpace(ref)
		if cfg != nil {
			if e, ok := cfg.ResolveModel(ref); ok {
				return e.Name + "/" + e.Model
			}
		}
		return ref
	}
	if canonical(previous) != canonical(next) {
		return nil
	}
	value := *effort
	return &value
}
