package main

import (
	"os"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/provider"
)

func TestVisionSelectionUsesDisplayedCapability(t *testing.T) {
	on, off := true, false
	for _, tt := range []struct {
		name     string
		model    string
		baseURL  string
		request  string
		override *bool
		cached   bool
		want     config.CapabilityState
	}{
		{"manual gateway qwen", "qwen3.8-max", "https://gateway.invalid/v1", "", &on, false, config.CapabilitySupported},
		{"manual gateway deepseek", "deepseek-v4.1-flash-1017", "https://gateway.invalid/v1", "", &on, false, config.CapabilitySupported},
		{"builtin metadata", "kimi-k3", "https://opencode.ai/zen/go/v1", "", nil, false, config.CapabilitySupported},
		{"discovered metadata", "opaque-vision", "https://gateway.invalid/v1", "", nil, true, config.CapabilitySupported},
		{"explicit off beats discovery", "opaque-vision", "https://gateway.invalid/v1", "", &off, true, config.CapabilityUnsupported},
		{"undeclared saved model", "opaque-chat", "https://gateway.invalid/v1", "", nil, false, config.CapabilityUnsupported},
		{"official text model", "deepseek-v4-pro", "https://api.deepseek.com", "", &on, false, config.CapabilityUnsupported},
		{"official request endpoint", "deepseek-v4-pro", "https://gateway.invalid/v1", "https://api.deepseek.com/chat/completions", &on, false, config.CapabilityUnsupported},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			t.Setenv("REASONIX_HOME", t.TempDir())
			setDesktopTestCredential(t, "VISION_SELECTION_KEY", "test-key")
			view := ProviderView{
				Name: "gateway", Kind: "openai", BaseURL: tt.baseURL, RequestURL: tt.request,
				APIKeyEnv: "VISION_SELECTION_KEY", Models: []string{tt.model},
			}
			if tt.override != nil {
				view.ModelOverrides = []ProviderModelOverrideView{{Model: tt.model, Vision: tt.override}}
			}
			if err := NewApp().SaveProvider(view); err != nil {
				t.Fatal(err)
			}
			cfg := config.LoadForEdit(config.UserConfigPath())
			entry, ok := cfg.ResolveModel("gateway/" + tt.model)
			if !ok {
				t.Fatal("saved model not found")
			}
			if tt.cached {
				config.NewModelCapabilityResolver().PutCatalog(*entry, []provider.ModelInfo{{
					ID: tt.model, InputModalities: []provider.ModelModality{provider.ModalityText, provider.ModalityImage},
				}})
			}
			capabilities := providerModelCapabilitiesForView(*entry, []string{tt.model})
			if len(capabilities) != 1 || capabilities[0].State != string(tt.want) {
				t.Fatalf("displayed capability = %+v, want %s", capabilities, tt.want)
			}
			before, err := os.ReadFile(config.UserConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			// A fresh app must use persisted capability settings and discovery,
			// independent of the provider editor's in-memory state.
			err = NewApp().SetVisionModel(" gateway/" + tt.model + " ")
			rpcErr := applyModelPreference(cfg, ModelSettingsChange{Kind: "preference", Field: "vision", Ref: "gateway/" + tt.model})
			cliErr := cfg.SetVisionModel("gateway/" + tt.model)
			if tt.want == config.CapabilitySupported {
				if err != nil || rpcErr != nil || cliErr != nil {
					t.Fatalf("supported model rejected: desktop=%v RPC=%v CLI=%v", err, rpcErr, cliErr)
				}
				saved := config.LoadForEdit(config.UserConfigPath())
				if saved.Agent.VisionModel != "gateway/"+tt.model {
					t.Fatalf("saved vision model = %q", saved.Agent.VisionModel)
				}
			} else {
				if err == nil || rpcErr == nil || cliErr == nil {
					t.Fatalf("non-supported model accepted: desktop=%v RPC=%v CLI=%v", err, rpcErr, cliErr)
				}
				after, err := os.ReadFile(config.UserConfigPath())
				if err != nil || string(before) != string(after) {
					t.Fatalf("rejected selection changed saved config: %v", err)
				}
			}
		})
	}
}
