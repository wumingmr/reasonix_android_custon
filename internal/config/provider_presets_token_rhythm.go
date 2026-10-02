package config

var tokenRhythmModels = []string{
	"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-pro", "glm-5", "glm-5.1",
	"minimax-m2.7", "kimi-k2.5", "kimi-k2.6", "minimax-m2.5",
	"mimo-v2.5-pro", "qwen3.7-max", "kimi-k2.7-code", "glm-5.2",
	"qwen3.8-max", "deepseek-v4-flash-0731",
}

var tokenRhythmVisionModels = []string{"kimi-k2.5", "kimi-k2.6", "kimi-k2.7-code"}

func tokenRhythmModelOverrides() map[string]ProviderModelOverride {
	return map[string]ProviderModelOverride{
		"deepseek-flash": {
			ReasoningProtocol: ReasoningProtocolDeepSeek,
			SupportedEfforts:  []string{"disabled", "low", "high", "max"},
			DefaultEffort:     "high",
		},
		"deepseek-v4-flash": {
			ReasoningProtocol: ReasoningProtocolDeepSeek,
			SupportedEfforts:  []string{"disabled", "low", "high", "max"},
			DefaultEffort:     "high",
		},
		"deepseek-v4-pro": {
			ReasoningProtocol: ReasoningProtocolDeepSeek,
			SupportedEfforts:  []string{"disabled", "high", "max"},
			DefaultEffort:     "high",
		},
		"deepseek-v4-flash-0731": {
			ReasoningProtocol: ReasoningProtocolDeepSeek,
			SupportedEfforts:  []string{"disabled", "low", "high", "max"},
			DefaultEffort:     "high",
		},
		"glm-5": {
			ReasoningProtocol: ReasoningProtocolGLM,
			SupportedEfforts:  []string{"enabled", "disabled"},
			DefaultEffort:     "enabled",
		},
		"glm-5.1": {
			ReasoningProtocol: ReasoningProtocolGLM,
			SupportedEfforts:  []string{"enabled", "disabled"},
			DefaultEffort:     "enabled",
			ContextWindow:     200_000,
		},
		"glm-5.2": {
			ReasoningProtocol: ReasoningProtocolGLM,
			SupportedEfforts:  []string{"enabled", "disabled"},
			DefaultEffort:     "enabled",
		},
		"minimax-m2.7":   {ContextWindow: 200_000},
		"kimi-k2.5":      {ContextWindow: 256_000},
		"kimi-k2.6":      {ContextWindow: 256_000},
		"minimax-m2.5":   {ContextWindow: 200_000},
		"mimo-v2.5-pro":  {ContextWindow: 256_000},
		"kimi-k2.7-code": {ContextWindow: 256_000},
	}
}
