package config

import "strings"

// acceptsDeepSeekModelReference preserves saved session references to retired
// official Flash IDs without putting aliases back in the selectable catalog.
// Resolution keeps the original wire ID, pricing and model-specific overrides.
func acceptsDeepSeekModelReference(p *ProviderEntry, model string) bool {
	if p.HasModel(model) {
		return true
	}
	if !p.HasModel("deepseek-flash") || !isOfficialDeepSeekModelReferenceEndpoint(p) {
		return false
	}
	switch model {
	case "deepseek-v4-flash", "deepseek-v4-flash-vision-exp":
		return true
	default:
		return false
	}
}

func normalizeOfficialDeepSeekModels(c *Config) {
	if c == nil {
		return
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if officialProviderHost(p.BaseURL) != "api.deepseek.com" {
			continue
		}
		// Explicit lists belong to the user. Legacy singular entries still
		// receive the stock catalog while retaining their original model.
		if len(p.Models) == 0 {
			legacyModel := p.Model
			legacyDefault := p.DefaultModel()
			switch strings.TrimSpace(p.Name) {
			case "deepseek", "deepseek-chat", "deepseek-anthropic", "deepseek-responses":
				ensureProviderModels(p, deepSeekOfficialModels, "deepseek-flash")
			case "deepseek-flash":
				ensureProviderModels(p, []string{"deepseek-flash"}, "deepseek-flash")
			case "deepseek-pro":
				ensureProviderModels(p, []string{"deepseek-v4-pro"}, "deepseek-v4-pro")
			}
			// A singular price belongs to the original model ID. Keep that
			// provenance for billing migrations and saved legacy references.
			if legacyModel != "" {
				p.Model = legacyModel
				if len(p.Models) > 0 && p.HasModel(legacyDefault) {
					p.Default = legacyDefault
				}
			}
		}
		backfillOfficialDeepSeekVisionPrice(p)
		if isOfficialDeepSeekResponsesProvider(p) {
			backfillOfficialDeepSeekResponsesProPrice(p)
		}
		backfillDeepSeekAnthropicCapabilities(p)
	}
}

func isOfficialDeepSeekModelReferenceEndpoint(p *ProviderEntry) bool {
	endpoint, exact := normalizedExactProviderRequestURL(ProviderEffectiveRequestURL(p))
	if !exact {
		return false
	}
	switch normalizedProviderProtocol(p.Kind) {
	case "openai":
		return endpoint == "https://api.deepseek.com/chat/completions" || endpoint == "https://api.deepseek.com/v1/chat/completions"
	case "responses":
		return endpoint == "https://api.deepseek.com/responses" || endpoint == "https://api.deepseek.com/v1/responses"
	case "anthropic":
		return endpoint == "https://api.deepseek.com/anthropic/v1/messages"
	default:
		return false
	}
}
