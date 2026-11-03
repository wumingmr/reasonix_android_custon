package cli

import (
	"fmt"
	"os"
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
)

func cloneSetupOverridesWithoutDiscovery(in map[string]config.ProviderModelOverride) map[string]config.ProviderModelOverride {
	out := make(map[string]config.ProviderModelOverride, len(in))
	for model, ov := range in {
		ov.DiscoveredReasoning = nil
		if ov.ReasoningProtocol == "" && len(ov.SupportedEfforts) == 0 && ov.DefaultEffort == "" && ov.Effort == nil && ov.Vision == nil && ov.ContextWindow == 0 && ov.MaxOutputTokens == 0 {
			continue
		}
		out[model] = ov
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *providerSetupSession) hasReasoningCatalog() bool {
	for _, d := range s.imageCatalogs {
		for _, model := range d.models {
			if model.ReasoningInfo != nil || d.entry.ModelOverrides[model.ID].DiscoveredReasoning != nil {
				return true
			}
		}
	}
	return false
}

// Run inside setup's commit after credential rotation so the stored identity
// follows the accepted route. Discovery never overwrites a manual declaration.
func (s *providerSetupSession) applyReasoningCatalogs(fresh *config.Config) {
	for name, d := range s.imageCatalogs {
		draft, ok := s.cfg.Provider(name)
		if !ok || !config.ProviderEntriesConfigEqual(*draft, d.entry) {
			continue
		}
		entry, ok := fresh.Provider(name)
		if !ok {
			continue
		}
		copy := config.ProviderEntryConfigSnapshot(*entry)
		if _, rotated := s.pendingConnectionCredentials[name]; rotated {
			copy.APIKeyEnv = d.entry.APIKeyEnv
		}
		if !config.ProviderEntriesConfigEqual(copy, d.entry) {
			continue
		}
		entry.ApplyDiscoveredReasoning(d.models)
	}
}

func setupReasoningDescription(entry config.ProviderEntry, info provider.ModelInfo) string {
	entry.Model = info.ID
	cap := config.ReasoningCapabilityForEntry(&entry)
	r := info.ReasoningInfo
	if r != nil {
		if r.Invalid {
			return "effort: " + i18n.M.SetupReasoningInvalid
		}
		if len(r.Efforts) > 0 {
			status := " · " + i18n.M.SetupReasoningEndpoint
			if !config.DiscoveredReasoningUsable(&entry, r) {
				status += " (" + i18n.M.SetupReasoningUnusable + ")"
			}
			if r.Default != "" {
				status += " · " + fmt.Sprintf(i18n.M.SetupReasoningDefaultFmt, r.Default)
			}
			return "effort: " + strings.Join(r.Efforts, "/") + status
		}
		if r.Parameter || (r.Supported != nil && *r.Supported) {
			return "effort: " + i18n.M.SetupReasoningParameter + " · " + i18n.M.SetupReasoningEndpoint
		}
		if r.Supported != nil || r.Protocol == "none" {
			return "effort: " + i18n.M.SetupReasoningUnsupported + " · " + i18n.M.SetupReasoningEndpoint
		}
		if r.Protocol != "" {
			return "effort: " + i18n.M.SetupReasoningUnknown + " · " + r.Protocol + " · " + i18n.M.SetupReasoningEndpoint
		}
	}
	if cap.Unknown {
		return "effort: " + i18n.M.SetupReasoningUnknown
	}
	if len(cap.Options) == 0 {
		return "effort: " + i18n.M.SetupReasoningUnsupported
	}
	return "effort: " + strings.Join(cap.IDs(), "/") + " · " + i18n.M.SetupReasoningConfigured
}

var selectSetupEffort = selectOne

func editProviderModelEfforts(s *providerSetupSession, p config.ProviderEntry) {
	items := make([]menuItem, 0, len(p.ModelList()))
	preview := config.ProviderEntryConfigSnapshot(p)
	if discovery, ok := s.imageCatalogs[p.Name]; ok && config.ProviderEntriesConfigEqual(p, discovery.entry) {
		preview.ApplyDiscoveredReasoning(discovery.models)
	}
	previewConfig := &config.Config{Providers: []config.ProviderEntry{preview}}
	for _, model := range p.ModelList() {
		e, _ := previewConfig.ResolveModel(p.Name + "/" + model)
		items = append(items, menuItem{name: model, desc: "effort: " + config.EffortDisplay(e)})
	}
	idx, err := selectSetupEffort(i18n.M.SetupEffortSelectModel, items)
	if err != nil {
		return
	}
	model := p.ModelList()[idx]
	e, _ := previewConfig.ResolveModel(p.Name + "/" + model)
	cap := config.ReasoningCapabilityForEntry(e)
	choices := []menuItem{{name: "auto", desc: i18n.M.SetupEffortAutoHint}, {name: "inherit", desc: i18n.M.SetupEffortInheritHint}}
	for _, level := range cap.Options {
		choices = append(choices, menuItem{name: level.ID, desc: level.Description})
	}
	choice, err := selectSetupEffort(fmt.Sprintf(i18n.M.SetupEffortSelectFmt, model), choices)
	if err != nil {
		return
	}
	before := p
	cfg := previewConfig
	var value *string
	if choice != 1 {
		v := choices[choice].name
		if choice == 0 {
			v = ""
		}
		value = &v
	}
	if err := cfg.SetModelEffort(p.Name, model, value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	// Keep discovery staged until Save; only the user's effort edit enters draft.
	ov := before.ModelOverrides[model]
	ov.Effort = cfg.Providers[0].ModelOverrides[model].Effort
	edited := config.ProviderEntryConfigSnapshot(before)
	edited.ModelOverrides = make(map[string]config.ProviderModelOverride, len(before.ModelOverrides)+1)
	for key, value := range before.ModelOverrides {
		edited.ModelOverrides[key] = value
	}
	edited.ModelOverrides[model] = ov
	if err := s.upsert([]config.ProviderEntry{edited}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if discovery, ok := s.imageCatalogs[p.Name]; ok && config.ProviderEntriesConfigEqual(before, discovery.entry) {
		current, _ := s.cfg.Provider(p.Name)
		discovery.entry = config.ProviderEntryConfigSnapshot(*current)
		s.imageCatalogs[p.Name] = discovery
	}
}
