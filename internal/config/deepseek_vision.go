package config

import (
	"slices"

	"reasonix/internal/provider"
	"reasonix/internal/provider/openai"
)

// OfficialDeepSeekPinnedVisionModels returns the official vision SKU when it is
// in the enabled model list. Settings can still mark other models for image
// input; this helper is only the stock default.
func OfficialDeepSeekPinnedVisionModels(models []string) []string {
	if !slices.ContainsFunc(models, openai.IsOfficialDeepSeekVisionModel) {
		return nil
	}
	return []string{openai.OfficialDeepSeekVisionModel}
}

func backfillOfficialDeepSeekVisionPrice(p *ProviderEntry) {
	sku := openai.OfficialDeepSeekVisionModel
	if p == nil || !p.HasModel(sku) {
		return
	}
	if price, ok := pricingForModelKey(p.Prices, sku); ok && price != nil {
		return
	}
	currency := p.ProviderBillingCurrency()
	if currency == "" {
		currency = "USD"
	}
	price := deepSeekV4PriceForModel(currency, sku)
	if price == nil {
		return
	}
	if p.Prices == nil {
		p.Prices = map[string]*provider.Pricing{}
	}
	p.Prices[sku] = price
}
