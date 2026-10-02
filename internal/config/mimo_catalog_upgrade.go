package config

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	fileencoding "reasonix/internal/fileutil/encoding"
)

const mimoCatalogUpgradeVersion = 12

type mimoCatalogProviderUpdate struct {
	models       []string
	visionModels []string
	prices       map[string]map[string]any
}

// The caller holds the config edit lock. Model additions and the migration
// marker are committed together so a model the user later removes stays gone.
func upgradeMimoCatalogFileLocked(path string, write func(string, []byte, os.FileMode) error) (bool, error) {
	resolved, exists, err := statConfigPath(path)
	if err != nil || !exists {
		return false, err
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return false, err
	}
	encoding, data := fileencoding.Detect(raw)
	body := string(fileencoding.Decode(data, encoding))
	crlf := strings.Contains(body, "\r\n")
	if crlf {
		body = strings.ReplaceAll(body, "\r\n", "\n")
	}
	next, changed, err := rewriteMimoCatalogUpgrade(body)
	if err != nil || !changed {
		return false, err
	}
	if crlf {
		next = strings.ReplaceAll(next, "\n", "\r\n")
	}
	encoded, err := fileencoding.Encode(next, encoding)
	if err != nil {
		return false, fmt.Errorf("commit MiMo catalog upgrade: %w", err)
	}
	if err := write(resolved, encoded, info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("commit MiMo catalog upgrade: %w", err)
	}
	return true, nil
}

func rewriteMimoCatalogUpgrade(body string) (string, bool, error) {
	var cfg Config
	if _, err := toml.Decode(body, &cfg); err != nil {
		return body, false, err
	}
	if cfg.ConfigVersion >= mimoCatalogUpgradeVersion {
		return body, false, nil
	}
	var expected map[string]any
	if _, err := toml.Decode(body, &expected); err != nil {
		return body, false, err
	}
	expected["config_version"] = int64(mimoCatalogUpgradeVersion)
	providerTables, _ := deepSeekCatalogDocumentValue(expected["providers"]).([]any)
	updates := make(map[int]mimoCatalogProviderUpdate)
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !isOfficialMimoCatalogUpgradeEntry(p) || len(p.ModelList()) == 0 ||
			(!p.HasModel("mimo-v2.5-pro") && !p.HasModel("mimo-v2.5")) {
			continue
		}
		update := mimoCatalogProviderUpdate{models: append([]string(nil), p.ModelList()...)}
		for _, model := range []string{"mimo-v2.6-pro", "mimo-v2.6-flash"} {
			if !p.HasModel(model) {
				update.models = append(update.models, model)
			}
		}
		if stringSlicesEqual(p.VisionModels, []string{"mimo-v2.5"}) {
			update.visionModels = append(append([]string(nil), p.VisionModels...), "mimo-v2.6-pro", "mimo-v2.6-flash")
		}
		missingPrices := make([]string, 0, 2)
		for _, model := range []string{"mimo-v2.6-pro", "mimo-v2.6-flash"} {
			if p.Prices[model] == nil {
				missingPrices = append(missingPrices, model)
			}
		}
		update.prices = mimoCatalogPriceDocuments(missingPrices)
		if stringSlicesEqual(update.models, p.ModelList()) && update.visionModels == nil && len(update.prices) == 0 {
			continue
		}
		updates[i] = update

		fields, ok := providerTables[i].(map[string]any)
		if !ok {
			return body, false, fmt.Errorf("MiMo catalog upgrade cannot map provider %d", i)
		}
		fields["models"] = stringSliceDocument(update.models)
		if update.visionModels != nil {
			fields["vision_models"] = stringSliceDocument(update.visionModels)
		}
		if len(update.prices) > 0 {
			table, _ := fields["prices"].(map[string]any)
			if table == nil {
				table = make(map[string]any)
				fields["prices"] = table
			}
			for model, price := range update.prices {
				encoded, err := rawTOMLValue(price)
				if err != nil {
					return body, false, err
				}
				var rendered map[string]any
				if _, err := toml.Decode("price = "+encoded, &rendered); err != nil {
					return body, false, err
				}
				table[model] = rendered["price"]
			}
		}
	}
	next := body
	var err error
	if len(updates) > 0 {
		next, err = rewriteMimoCatalogProviders(next, len(cfg.Providers), updates)
		if err != nil {
			return body, false, err
		}
	}
	next, err = rawTOMLSet(next, []string{"config_version"}, mimoCatalogUpgradeVersion)
	if err != nil {
		return body, false, err
	}
	var actual map[string]any
	if _, err := toml.Decode(next, &actual); err != nil {
		return body, false, fmt.Errorf("MiMo catalog upgrade readback: %w", err)
	}
	if !reflect.DeepEqual(deepSeekCatalogDocumentValue(actual), deepSeekCatalogDocumentValue(expected)) {
		return body, false, fmt.Errorf("MiMo catalog upgrade changed unplanned fields; original configuration retained")
	}
	return next, true, nil
}

func isOfficialMimoCatalogUpgradeEntry(p *ProviderEntry) bool {
	if p == nil || strings.TrimSpace(p.RequestURL) != "" || strings.TrimSpace(p.ChatURL) != "" {
		return false
	}
	host := officialProviderHost(p.BaseURL)
	switch host {
	case "api.xiaomimimo.com", "token-plan-cn.xiaomimimo.com", "token-plan-sgp.xiaomimimo.com", "token-plan-ams.xiaomimimo.com":
	default:
		return false
	}
	suffix := "/v1"
	if normalizedProviderProtocol(p.Kind) == "anthropic" {
		suffix = "/anthropic"
	} else if normalizedProviderProtocol(p.Kind) != "openai" && normalizedProviderProtocol(p.Kind) != "responses" {
		return false
	}
	return normalizedBaseURLForMigration(p.BaseURL) == "https://"+host+suffix
}

func stringSliceDocument(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func mimoCatalogPriceDocuments(models []string) map[string]map[string]any {
	out := make(map[string]map[string]any)
	for model, price := range mimoDomesticPrices(models) {
		out[model] = map[string]any{
			"cache_hit": price.CacheHit,
			"input":     price.Input,
			"output":    price.Output,
			"currency":  price.Currency,
		}
	}
	return out
}

func rewriteMimoCatalogProviders(body string, count int, updates map[int]mimoCatalogProviderUpdate) (string, error) {
	lines := strings.Split(body, "\n")
	blocks := providerTOMLBlocks(lines)
	if len(blocks) != count {
		expanded, err := expandOpenCodeGoInlineProviders(body)
		if err != nil {
			return body, err
		}
		if len(providerTOMLBlocks(strings.Split(expanded, "\n"))) != count {
			return body, fmt.Errorf("MiMo catalog upgrade cannot map provider tables")
		}
		return rewriteMimoCatalogProviders(expanded, count, updates)
	}
	for i := range slices.Backward(blocks) {
		update, ok := updates[i]
		if !ok {
			continue
		}
		b := blocks[i]
		for b.end < len(lines) {
			header := tomlSectionHeader(lines[b.end])
			path := rawTOMLKeyPath(strings.Trim(header, "[]"))
			if header != "" && (len(path) < 2 || path[0] != "providers") {
				break
			}
			b.end++
		}
		part := strings.Join(lines[b.start+1:b.end], "\n")
		var err error
		part, err = rawTOMLSet(part, []string{"models"}, update.models)
		if err != nil {
			return body, err
		}
		if update.visionModels != nil {
			part, err = rawTOMLSet(part, []string{"vision_models"}, update.visionModels)
			if err != nil {
				return body, err
			}
		}
		part, err = appendMimoCatalogPriceTables(part, update.prices)
		if err != nil {
			return body, err
		}
		lines = append(lines[:b.start+1], append(strings.Split(part, "\n"), lines[b.end:]...)...)
	}
	return strings.Join(lines, "\n"), nil
}

func appendMimoCatalogPriceTables(body string, prices map[string]map[string]any) (string, error) {
	for _, model := range []string{"mimo-v2.6-pro", "mimo-v2.6-flash"} {
		price := prices[model]
		if price == nil {
			continue
		}
		// Write leaves so inline prices are extended in place and absent prices
		// create distinct model tables, without redeclaring an implicit parent.
		for _, key := range []string{"cache_hit", "input", "output", "currency"} {
			var err error
			body, err = rawTOMLSet(body, []string{"providers", "prices", model, key}, price[key])
			if err != nil {
				return body, err
			}
		}
	}
	return body, nil
}
