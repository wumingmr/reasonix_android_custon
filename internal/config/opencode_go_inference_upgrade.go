package config

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	fileencoding "reasonix/internal/fileutil/encoding"
	"reasonix/internal/provider"
)

const openCodeGoInferenceUpgradeVersion = 13

// The startup edit lock covers the endpoint/model additions and their marker.
// Regular loads never reapply this upgrade or resurrect a deleted model.
func upgradeOpenCodeGoInferenceFileLocked(path string, write func(string, []byte, os.FileMode) error) (bool, error) {
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
	next, changed, err := rewriteOpenCodeGoInferenceUpgrade(body)
	if err != nil || !changed {
		return false, err
	}
	if crlf {
		next = strings.ReplaceAll(next, "\n", "\r\n")
	}
	encoded, err := fileencoding.Encode(next, encoding)
	if err != nil {
		return false, err
	}
	if err := write(resolved, encoded, info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("commit OpenCode Go inference upgrade: %w", err)
	}
	return true, nil
}

func rewriteOpenCodeGoInferenceUpgrade(body string) (string, bool, error) {
	var before, after Config
	if _, err := toml.Decode(body, &before); err != nil {
		return body, false, err
	}
	if before.ConfigVersion >= openCodeGoInferenceUpgradeVersion {
		return body, false, nil
	}
	if _, err := toml.Decode(body, &after); err != nil {
		return body, false, err
	}
	var expected map[string]any
	if _, err := toml.Decode(body, &expected); err != nil {
		return body, false, err
	}
	expected["config_version"] = int64(openCodeGoInferenceUpgradeVersion)
	tables, _ := deepSeekCatalogDocumentValue(expected["providers"]).([]any)
	for i := range after.Providers {
		p := &after.Providers[i]
		route, _ := openCodeGoMigrationRoute(*p)
		if route == "" {
			continue
		}
		base := provider.OpenCodeGoChatBaseURL
		if route == provider.OpenCodeGoRouteAnthropic {
			base = provider.OpenCodeGoAnthropicBaseURL
		}
		p.BaseURL = base
		if p.RequestURL != "" {
			p.RequestURL = ProviderRequestURL(p.Kind, base)
		}
		if p.ChatURL != "" {
			p.ChatURL = provider.OpenCodeGoChatBaseURL + "/chat/completions"
		}
		if p.ModelsURL != "" {
			if canonical, ok := provider.CanonicalOpenCodeGoModelsURL(p.ModelsURL); ok {
				p.ModelsURL = canonical
			}
		}
		// The new SKU is recommended on Chat Completions. Keep existing model
		// selections and search-capable alternate routes intact.
		if route == provider.OpenCodeGoRouteChat && !p.HasModel("deepseek-v4.1-flash") &&
			slices.ContainsFunc(p.ModelList(), provider.OpenCodeGoDeepSeekModel) {
			p.Models = append(append([]string(nil), p.ModelList()...), "deepseek-v4.1-flash")
			if p.Price != nil && p.Prices["deepseek-v4.1-flash"] == nil {
				if p.Prices == nil {
					p.Prices = map[string]*provider.Pricing{}
				}
				// Go's documented USD peak list rate; never inherit a
				// singular custom/Pro price for the newly added Flash SKU.
				p.Prices["deepseek-v4.1-flash"] = deepSeekV4FlashPriceUSD()
			}
		}
		fields := tables[i].(map[string]any)
		old := before.Providers[i]
		for _, pair := range []struct {
			key  string
			a, b any
		}{
			{"base_url", old.BaseURL, p.BaseURL}, {"request_url", old.RequestURL, p.RequestURL},
			{"chat_url", old.ChatURL, p.ChatURL}, {"models_url", old.ModelsURL, p.ModelsURL}, {"models", old.Models, p.Models},
		} {
			if !reflect.DeepEqual(pair.a, pair.b) {
				encoded, err := rawTOMLValue(pair.b)
				if err != nil {
					return body, false, err
				}
				var value map[string]any
				if _, err := toml.Decode("v = "+encoded, &value); err != nil {
					return body, false, err
				}
				fields[pair.key] = value["v"]
			}
		}
		if !reflect.DeepEqual(old.Prices, p.Prices) {
			prices, _ := fields["prices"].(map[string]any)
			if prices == nil {
				prices = map[string]any{}
				fields["prices"] = prices
			}
			price := p.Prices["deepseek-v4.1-flash"]
			prices["deepseek-v4.1-flash"] = map[string]any{"cache_hit": price.CacheHit, "input": price.Input, "output": price.Output, "currency": price.Currency}
		}
	}
	next, err := rewriteOpenCodeGoConfig(body, &before, &after, nil)
	if err != nil {
		return body, false, err
	}
	next, err = rawTOMLSet(next, []string{"config_version"}, openCodeGoInferenceUpgradeVersion)
	if err != nil {
		return body, false, err
	}
	var actual map[string]any
	if _, err := toml.Decode(next, &actual); err != nil {
		return body, false, err
	}
	if !reflect.DeepEqual(deepSeekCatalogDocumentValue(expected), deepSeekCatalogDocumentValue(actual)) {
		return body, false, fmt.Errorf("OpenCode Go inference upgrade changed unplanned fields; original configuration retained")
	}
	return next, true, nil
}
