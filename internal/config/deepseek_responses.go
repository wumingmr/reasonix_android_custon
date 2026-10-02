package config

import (
	"strings"

	"reasonix/internal/provider"
	"reasonix/internal/provider/openai"
)

func deepSeekV4EffortOverrides() map[string]ProviderModelOverride {
	flash := ProviderModelOverride{SupportedEfforts: []string{"disabled", "low", "high", "max"}, DefaultEffort: "high"}
	return map[string]ProviderModelOverride{
		"deepseek-flash":                   flash,
		"deepseek-v4-flash":                flash,
		openai.OfficialDeepSeekVisionModel: flash,
		"deepseek-v4-pro":                  {SupportedEfforts: []string{"disabled", "low", "high", "max"}, DefaultEffort: "high"},
	}
}

func isOfficialDeepSeekResponsesProvider(p *ProviderEntry) bool {
	return p != nil && strings.EqualFold(strings.TrimSpace(p.Kind), "responses") &&
		officialProviderHost(p.BaseURL) == "api.deepseek.com"
}

// backfillOfficialDeepSeekResponsesProPrice fills Prices[pro] when a legacy
// singular Price would otherwise make Pro inherit the Flash list price.
func backfillOfficialDeepSeekResponsesProPrice(p *ProviderEntry) {
	if p == nil || !p.HasModel("deepseek-v4-pro") || p.Prices["deepseek-v4-pro"] != nil {
		return
	}
	currency := p.ProviderBillingCurrency()
	if currency == "" {
		currency = "USD"
	}
	price := deepSeekV4PriceForModel(currency, "deepseek-v4-pro")
	if price == nil {
		return
	}
	if p.Prices == nil {
		p.Prices = map[string]*provider.Pricing{}
	}
	p.Prices["deepseek-v4-pro"] = price
}
