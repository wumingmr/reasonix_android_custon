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

const deepSeekCatalogUpgradeVersion = 11

// The caller holds the config edit lock. The model addition and version marker
// are committed atomically, so deleting the option afterwards is permanent.
func upgradeDeepSeekCatalogFileLocked(path string, write func(string, []byte, os.FileMode) error) (bool, error) {
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
	next, changed, err := rewriteDeepSeekCatalogUpgrade(body)
	if err != nil || !changed {
		return false, err
	}
	if crlf {
		next = strings.ReplaceAll(next, "\n", "\r\n")
	}
	encoded, err := fileencoding.Encode(next, encoding)
	if err != nil {
		return false, fmt.Errorf("commit DeepSeek catalog upgrade: %w", err)
	}
	if err := write(resolved, encoded, info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("commit DeepSeek catalog upgrade: %w", err)
	}
	return true, nil
}

func rewriteDeepSeekCatalogUpgrade(body string) (string, bool, error) {
	var cfg Config
	if _, err := toml.Decode(body, &cfg); err != nil {
		return body, false, err
	}
	if cfg.ConfigVersion >= deepSeekCatalogUpgradeVersion {
		return body, false, nil
	}
	var expected map[string]any
	if _, err := toml.Decode(body, &expected); err != nil {
		return body, false, err
	}
	expected["config_version"] = int64(deepSeekCatalogUpgradeVersion)
	providerTables, _ := deepSeekCatalogDocumentValue(expected["providers"]).([]any)
	updates := make(map[int][]string)
	prices := make(map[int]map[string]any)
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if officialProviderHost(p.BaseURL) != "api.deepseek.com" || !isOfficialDeepSeekModelReferenceEndpoint(p) ||
			len(p.ModelList()) == 0 || p.HasModel("deepseek-flash") {
			continue
		}
		// Append, rather than prepend, to preserve implicit first-model defaults.
		updates[i] = append(append([]string(nil), p.ModelList()...), "deepseek-flash")
		fields := providerTables[i].(map[string]any)
		models := make([]any, len(updates[i]))
		for j, model := range updates[i] {
			models[j] = model
		}
		fields["models"] = models
		// A Pro-only legacy connection can carry a singular Pro price. The new
		// Flash option must not inherit it; existing user rates remain untouched.
		if p.Price != nil && p.Prices["deepseek-flash"] == nil {
			currency := p.ProviderBillingCurrency()
			if currency == "" {
				currency = "USD"
			}
			price := deepSeekV4PriceForModel(currency, "deepseek-flash")
			prices[i] = map[string]any{"cache_hit": price.CacheHit, "input": price.Input, "output": price.Output, "currency": price.Currency}
			encoded, err := rawTOMLValue(prices[i])
			if err != nil {
				return body, false, err
			}
			var rendered map[string]any
			if _, err := toml.Decode("price = "+encoded, &rendered); err != nil {
				return body, false, err
			}
			table, _ := fields["prices"].(map[string]any)
			if table == nil {
				table = make(map[string]any)
				fields["prices"] = table
			}
			table["deepseek-flash"] = rendered["price"]
		}
	}
	next := body
	var err error
	if len(updates) > 0 {
		next, err = rewriteDeepSeekCatalogProviders(next, len(cfg.Providers), updates, prices)
		if err != nil {
			return body, false, err
		}
	}
	next, err = rawTOMLSet(next, []string{"config_version"}, deepSeekCatalogUpgradeVersion)
	if err != nil {
		return body, false, err
	}
	// Compare the entire generic document, including fields unknown to Config.
	// Only the planned model/price additions and version may change.
	var actual map[string]any
	if _, err := toml.Decode(next, &actual); err != nil {
		return body, false, fmt.Errorf("DeepSeek catalog upgrade readback: %w", err)
	}
	if !reflect.DeepEqual(deepSeekCatalogDocumentValue(actual), deepSeekCatalogDocumentValue(expected)) {
		return body, false, fmt.Errorf("DeepSeek catalog upgrade changed unplanned fields; original configuration retained")
	}
	return next, true, nil
}

// TOML decodes inline table arrays and array-of-table sections to different Go
// slice types. Compare their values without losing unknown fields or numbers.
func deepSeekCatalogDocumentValue(value any) any {
	switch v := value.(type) {
	case []map[string]any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = deepSeekCatalogDocumentValue(item)
		}
		return out
	case []any:
		for i, item := range v {
			v[i] = deepSeekCatalogDocumentValue(item)
		}
	case map[string]any:
		for key, item := range v {
			v[key] = deepSeekCatalogDocumentValue(item)
		}
	}
	return value
}

func rewriteDeepSeekCatalogProviders(body string, count int, models map[int][]string, prices map[int]map[string]any) (string, error) {
	lines := strings.Split(body, "\n")
	blocks := providerTOMLBlocks(lines)
	if len(blocks) != count {
		// Reuse the lexical inline-table expander; it retains unknown fields,
		// comments and string contents while changing structural separators.
		expanded, err := expandOpenCodeGoInlineProviders(body)
		if err != nil {
			return body, err
		}
		if len(providerTOMLBlocks(strings.Split(expanded, "\n"))) != count {
			return body, fmt.Errorf("DeepSeek catalog upgrade cannot map provider tables")
		}
		return rewriteDeepSeekCatalogProviders(expanded, count, models, prices)
	}
	for i := range slices.Backward(blocks) {
		if models[i] == nil {
			continue
		}
		b := blocks[i]
		// Include nested provider tables, but never an unrelated root table or
		// the next provider. rawTOMLSet preserves their existing assignments.
		for b.end < len(lines) {
			header := tomlSectionHeader(lines[b.end])
			path := rawTOMLKeyPath(strings.Trim(header, "[]"))
			if header != "" && (len(path) < 2 || path[0] != "providers") {
				break
			}
			b.end++
		}
		part := strings.Join(lines[b.start+1:b.end], "\n")
		part, err := rawTOMLSet(part, []string{"models"}, models[i])
		if err != nil {
			return body, err
		}
		if prices[i] != nil {
			part, err = rawTOMLSet(part, []string{"providers", "prices", "deepseek-flash"}, prices[i])
			if err != nil {
				return body, err
			}
		}
		lines = append(lines[:b.start+1], append(strings.Split(part, "\n"), lines[b.end:]...)...)
	}
	return strings.Join(lines, "\n"), nil
}
