package main

import "reasonix/internal/config"

func providerModelCapabilitiesForView(p config.ProviderEntry, models []string) []ProviderModelCapabilityView {
	resolver := config.NewModelCapabilityResolver()
	out := make([]ProviderModelCapabilityView, 0, len(models))
	for _, model := range models {
		entry := p
		entry.Model = model
		capability := resolver.Resolve(&entry)
		view := modelCapabilityView(capability)
		view.Reasoning = config.ResolveReasoningView(&entry)
		out = append(out, view)
	}
	return out
}

func modelCapabilityView(capability config.ResolvedModelCapability) ProviderModelCapabilityView {
	modalities := make([]string, len(capability.InputModalities))
	for i, modality := range capability.InputModalities {
		modalities[i] = string(modality)
	}
	return ProviderModelCapabilityView{
		Model: capability.Model, InputModalities: modalities,
		State: string(capability.State), Source: string(capability.Source),
		AutomaticState: string(capability.AutomaticState), AutomaticSource: string(capability.AutomaticSource),
		ImageInputEnableAllowed: capability.ImageInputEnableAllowed, ImageInputBlockReason: capability.ImageInputBlockReason,
	}
}
