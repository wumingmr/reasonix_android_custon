package config

import "testing"

func TestTokenRhythmPresetMatchesPublicAPIIntegration(t *testing.T) {
	preset, ok := CuratedProviderPreset("token-rhythm")
	if !ok || len(preset.Entries) != 1 {
		t.Fatalf("Token Rhythm preset = %+v, want one entry", preset)
	}
	if preset.Label != "Token Rhythm" || preset.KeyEnv != "TOKEN_RHYTHM_API_KEY" {
		t.Fatalf("Token Rhythm identity = label %q key %q", preset.Label, preset.KeyEnv)
	}
	entry := preset.Entries[0]
	if entry.Kind != "openai" || entry.BaseURL != "https://tokenrhythm.studio/v1" || entry.ModelsURL != "https://tokenrhythm.studio/v1/models" {
		t.Fatalf("Token Rhythm endpoint mismatch: %+v", entry)
	}
	if entry.DefaultModel() != "deepseek-v4-flash" || !entry.HasModel("qwen3.8-max") || entry.HasModel("qwen-image-2.0") {
		t.Fatalf("Token Rhythm chat catalog mismatch: models=%v default=%q", entry.Models, entry.DefaultModel())
	}

	var cfg Config
	if err := cfg.UpsertProvider(entry); err != nil {
		t.Fatalf("upsert Token Rhythm preset: %v", err)
	}
	deepseek, ok := cfg.ResolveModel("token-rhythm/deepseek-v4-flash")
	if !ok || deepseek.ContextWindow != 1_000_000 || ReasoningProtocolForEntry(deepseek) != ReasoningProtocolDeepSeek {
		t.Fatalf("Token Rhythm DeepSeek capability mismatch: %+v", deepseek)
	}
	shortFlash, ok := cfg.ResolveModel("token-rhythm/deepseek-flash")
	if !ok || ReasoningProtocolForEntry(shortFlash) != ReasoningProtocolDeepSeek {
		t.Fatalf("Token Rhythm DeepSeek short alias capability mismatch: %+v", shortFlash)
	}
	shortFlashCap := EffortCapabilityForEntry(shortFlash)
	if !shortFlashCap.Supported || shortFlashCap.Default != "high" || !stringSlicesEqual(shortFlashCap.Levels, []string{"auto", "disabled", "low", "high", "max"}) {
		t.Fatalf("Token Rhythm DeepSeek short alias effort mismatch: %+v", shortFlashCap)
	}
	kimi, ok := cfg.ResolveModel("token-rhythm/kimi-k2.7-code")
	if !ok || kimi.ContextWindow != 256_000 || !EffectiveVision(kimi) {
		t.Fatalf("Token Rhythm Kimi capability mismatch: %+v", kimi)
	}
	glm, ok := cfg.ResolveModel("token-rhythm/glm-5.1")
	if !ok || glm.ContextWindow != 200_000 || EffectiveVision(glm) || ReasoningProtocolForEntry(glm) != ReasoningProtocolGLM {
		t.Fatalf("Token Rhythm GLM capability mismatch: %+v", glm)
	}
	glmCap := EffortCapabilityForEntry(glm)
	if !glmCap.Supported || glmCap.Default != "enabled" || !stringSlicesEqual(glmCap.Levels, []string{"auto", "enabled", "disabled"}) {
		t.Fatalf("Token Rhythm GLM effort mismatch: %+v", glmCap)
	}
	flash0731, ok := cfg.ResolveModel("token-rhythm/deepseek-v4-flash-0731")
	if !ok || ReasoningProtocolForEntry(flash0731) != ReasoningProtocolDeepSeek {
		t.Fatalf("Token Rhythm DeepSeek 0731 protocol mismatch: %+v", flash0731)
	}
	flashCap := EffortCapabilityForEntry(flash0731)
	if !flashCap.Supported || flashCap.Default != "high" || !stringSlicesEqual(flashCap.Levels, []string{"auto", "disabled", "low", "high", "max"}) {
		t.Fatalf("Token Rhythm DeepSeek 0731 effort mismatch: %+v", flashCap)
	}
}
