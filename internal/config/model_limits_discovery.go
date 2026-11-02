package config

import (
	"strings"

	"reasonix/internal/provider"
)

// ApplyDiscoveredLimits writes what a model listing declared about each
// selected model into this entry's per-model overrides, and reports how many
// models it filled in.
//
// It only fills gaps. A window the user already declared — per model or for the
// whole connection — is left exactly as written, because a listing is the
// endpoint's claim and the configuration is the person's answer. An endpoint
// that reports no capacity for a model leaves that model inheriting, which is
// the same state it was in before the call.
func (e *ProviderEntry) ApplyDiscoveredLimits(catalog []provider.ModelInfo) int {
	if e == nil || len(catalog) == 0 {
		return 0
	}
	discovered := make(map[string]provider.ModelInfo, len(catalog))
	for _, info := range catalog {
		if id := strings.TrimSpace(info.ID); id != "" {
			discovered[id] = info
		}
	}
	filled := 0
	for _, model := range e.ModelList() {
		info, ok := discovered[strings.TrimSpace(model)]
		if !ok || (info.ContextWindow <= 0 && info.MaxOutputTokens <= 0) {
			continue
		}
		existing, declared := e.ModelLimits(model)
		if declared && existing.ContextWindow > 0 {
			// The model already states its own window; discovery adds nothing.
			continue
		}
		// A connection-wide window is the person's answer for every model it
		// covers, so a listing must not quietly replace it with per-model
		// overrides. Only a model that inherits nothing (no connection window and
		// no own window) has a real gap to fill.
		if !declared && e.ContextWindow > 0 && info.ContextWindow > 0 {
			continue
		}
		next := existing
		if info.ContextWindow > 0 {
			next.ContextWindow = info.ContextWindow
		}
		if info.MaxOutputTokens > 0 && next.MaxOutputTokens == 0 {
			next.MaxOutputTokens = info.MaxOutputTokens
		}
		if next == existing {
			continue
		}
		if err := e.SetModelLimits(model, next); err != nil {
			continue
		}
		filled++
	}
	return filled
}
