package provider_test

import (
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/provider/anthropic"
	"reasonix/internal/provider/openai"
	"reasonix/internal/provider/responses"
)

func TestAdaptersPreserveUnknownImageMetadata(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic", "responses"} {
		for _, mode := range []string{"unknown", "supported", "unsupported"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				info := provider.ModelInfo{ID: "custom-name-with-no-vision-suffix"}
				if mode != "unknown" {
					info.InputModalities = []provider.ModelModality{provider.ModalityText}
				}
				if mode == "supported" {
					info.InputModalities = append(info.InputModalities, provider.ModalityImage)
				}
				cfg := provider.Config{Name: "relay", BaseURL: "http://127.0.0.1:8790/v1", Model: info.ID, ModelInfo: &info}
				var p provider.Provider
				var err error
				switch kind {
				case "openai":
					p, err = openai.New(cfg)
				case "anthropic":
					p, err = anthropic.New(cfg)
				case "responses":
					p = responses.New(responses.Config{Name: cfg.Name, BaseURL: cfg.BaseURL, Model: cfg.Model, ModelInfo: cfg.ModelInfo})
				}
				if err != nil {
					t.Fatal(err)
				}
				got := p.(provider.ModelInfoProvider).ModelInfo()
				if (got.InputModalities == nil) != (mode == "unknown") || got.SupportsInput(provider.ModalityImage) != (mode == "supported") {
					t.Fatalf("metadata changed: %+v", got)
				}
				if info.ID != cfg.Model {
					t.Fatal("adapter mutated caller metadata")
				}
			})
		}
	}
}
