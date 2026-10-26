package provider

import (
	"reflect"
	"testing"
)

func TestOpenCodeGoInferenceRoutesAndDeepSeekV41(t *testing.T) {
	for _, tc := range []struct{ kind, base, endpoint, model, route string }{
		{"openai", "https://opencode.ai/inference/go/openai/v1", "chat/completions", "deepseek-v4.1-flash", OpenCodeGoRouteChat},
		{"anthropic", "https://opencode.ai/inference/go/anthropic", "v1/messages", "qwen3.7-plus", OpenCodeGoRouteAnthropic},
		{"responses", "https://opencode.ai/inference/go/openai/v1", "responses", "grok-4.5", OpenCodeGoRouteResponses},
		{"openai", "https://opencode.ai/zen/go/v1", "chat/completions", "deepseek-v4.1-flash", OpenCodeGoRouteChat},
	} {
		t.Run(tc.kind+tc.base, func(t *testing.T) {
			if route, ok := OfficialOpenCodeGoRoute(tc.kind, tc.base); !ok || route != tc.route {
				t.Fatalf("base route = %q, %v", route, ok)
			}
			requestURL := tc.base + "/" + tc.endpoint
			c, ok := LookupOpenCodeGoContract(tc.kind, "https://custom.example", requestURL, "", tc.model)
			if !ok || c.Route != tc.route {
				t.Fatalf("request contract = %+v, %v", c, ok)
			}
			if tc.model == "deepseek-v4.1-flash" && (c.ReasoningProtocol != "deepseek" || c.Thinking != "enabled" || c.Reasoning.Validate(tc.model, "low") != nil) {
				t.Fatalf("V4.1 lost thinking/replay contract: %+v", c)
			}
			for _, custom := range []string{requestURL + "?custom=1", requestURL + "/extra", "https://opencode.ai.evil/inference/go/openai/v1/chat/completions"} {
				if _, ok := LookupOpenCodeGoContract(tc.kind, tc.base, custom, "", tc.model); ok {
					t.Fatalf("custom endpoint inherited official contract: %s", custom)
				}
			}
		})
	}
}

func TestOpenCodeGoContractUsesExactEffectiveEndpointAndModel(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic", "responses"} {
		endpoint := map[string]string{"openai": "chat/completions", "anthropic": "messages", "responses": "responses"}[kind]
		url := "https://opencode.ai/zen/go/v1/" + endpoint
		for _, model := range []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"} {
			c, ok := LookupOpenCodeGoContract(kind, "https://custom.example", url, "", model)
			if !ok || c.RecommendedRoute != OpenCodeGoRouteChat || c.Reasoning.Default != "high" || c.Reasoning.Validate(model, "max") != nil {
				t.Fatalf("%s %s: %+v %v", kind, model, c, ok)
			}
			for _, custom := range []string{url + "?route=user", url + "#user", url + "/custom", "https://custom.example/v1/" + endpoint} {
				if _, ok := LookupOpenCodeGoContract(kind, "https://opencode.ai/zen/go/v1", custom, "", model); ok {
					t.Fatalf("custom endpoint inherited contract: %s", custom)
				}
			}
		}
		if _, ok := LookupOpenCodeGoContract(kind, "", url, "", "deepseek-v4-pro-custom"); ok {
			t.Fatal("unknown model was guessed from its prefix")
		}
		if _, ok := LookupOpenCodeGoContract(kind, "", url, "", "DeepSeek-v4-pro"); ok {
			t.Fatal("model IDs must be exact")
		}
	}
}

func TestOpenCodeGoDiscoveryFiltersTheActualRequestRoute(t *testing.T) {
	models := []string{"deepseek-v4-pro", "qwen3.8-max", "grok-4.5", "user-custom"}
	got := FilterOpenCodeGoRequestModels("openai", "https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go/v1/chat/completions", "", models)
	if !reflect.DeepEqual(got, []string{"deepseek-v4-pro", "qwen3.8-max"}) {
		t.Fatal(got)
	}
	got = FilterOpenCodeGoRequestModels("openai", "https://opencode.ai/zen/go/v1", "https://custom.example/v1/chat/completions", "", models)
	if !reflect.DeepEqual(got, models) {
		t.Fatal("custom discovery was filtered using an unrelated official base")
	}
}

func TestApplyOpenCodeGoContractDoesNotMutateCallerDeclarations(t *testing.T) {
	extra := map[string]any{"thinking": "enabled"}
	cfg := Config{BaseURL: "https://opencode.ai/zen/go", Model: "deepseek-v4-pro", Extra: extra}
	resolved := ApplyOpenCodeGoContract("anthropic", cfg)
	if len(extra) != 1 || resolved.Extra["reasoning_protocol"] != "deepseek" || resolved.Extra["default_effort"] != "high" {
		t.Fatalf("contract injection mutated caller or lost default: %+v", resolved.Extra)
	}
	extra["reasoning_protocol"] = "custom"
	extra["supported_efforts"] = []string{"custom-max"}
	resolved = ApplyOpenCodeGoContract("anthropic", cfg)
	if resolved.Extra["reasoning_protocol"] != "custom" || !reflect.DeepEqual(resolved.Extra["supported_efforts"], []string{"custom-max"}) {
		t.Fatal("explicit declaration was overwritten")
	}
}
