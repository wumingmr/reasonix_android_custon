package main

import (
	"testing"

	"reasonix/internal/config"
)

func TestSaveProviderPersistsWebSearchOffWithDisplayedRequestURL(t *testing.T) {
	for _, tc := range []struct{ name, kind, base, request string }{
		{"deepseek-responses", "responses", "https://api.deepseek.com", "https://api.deepseek.com/responses"},
		{"deepseek-anthropic", "anthropic", "https://api.deepseek.com/anthropic", "https://api.deepseek.com/anthropic/v1/messages"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			cfg := config.Default()
			cfg.Providers = []config.ProviderEntry{{
				Name: tc.name, PresetID: tc.name, Kind: tc.kind, BaseURL: tc.base,
				Model: "deepseek-v4-flash", Models: []string{"deepseek-v4-flash"},
			}}
			view := providerViewFromEntry(cfg.Providers[0], false, true)
			if !view.WebSearch {
				t.Fatalf("official endpoint should default search on: %+v", view)
			}
			view.RequestURL = tc.request
			view.WebSearch = false
			if err := saveProviderConfig(cfg, view); err != nil {
				t.Fatalf("save provider: %v", err)
			}
			got := cfg.Providers[0]
			if got.WebSearch == nil || *got.WebSearch {
				t.Fatalf("search switch not persisted off: %+v", got.WebSearch)
			}
			if providerViewFromEntry(got, false, true).WebSearch {
				t.Fatal("reloaded view reads search back as on")
			}
		})
	}
}

func TestSaveProviderDropsWebSearchForCustomRequestURL(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = []config.ProviderEntry{{
		Name: "deepseek-responses", PresetID: "deepseek-responses", Kind: "responses", BaseURL: "https://api.deepseek.com",
		Model: "deepseek-v4-flash", Models: []string{"deepseek-v4-flash"},
	}}
	view := providerViewFromEntry(cfg.Providers[0], false, true)
	view.RequestURL = "https://relay.example/responses"
	view.WebSearch = false
	if err := saveProviderConfig(cfg, view); err != nil {
		t.Fatalf("save provider: %v", err)
	}
	if got := cfg.Providers[0]; got.WebSearch != nil || config.IsOfficialDeepSeekSearchEndpoint(&got) {
		t.Fatalf("custom request URL kept official search state: %+v", got)
	}
}
