package config

import "testing"

func TestModelRuntimeContinuationRevocation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(*Config)
		allowed bool
	}{
		{"address", func(c *Config) { c.Providers[0].BaseURL = "https://new.invalid" }, true},
		{"provider removed", func(c *Config) { c.Providers = nil }, false},
		{"access removed", func(c *Config) { c.Desktop.ProviderAccess = []string{} }, false},
		{"model removed", func(c *Config) { c.Providers[0].Model = "other" }, false},
		{"credential rotated", func(c *Config) { c.Providers[0].APIKeyEnv = "MODEL_CONTINUATION_NEW" }, false},
		{"credential cleared", func(c *Config) { c.Providers[0].APIKeyEnv = "MODEL_CONTINUATION_EMPTY" }, false},
		{"credential header", func(c *Config) { c.Providers[0].Headers = map[string]string{"Authorization": "new"} }, false},
		{"credential URL", func(c *Config) { c.Providers[0].BaseURL = "https://user:secret@new.invalid" }, false},
		{"limit tightened", func(c *Config) { c.Agent.MaxSubagentDepth = 1 }, false},
		{"task budget tightened", func(c *Config) { c.Agent.TaskCostBudget = 1 }, false},
		{"model output capped", func(c *Config) { c.Providers[0].MaxOutputTokens = 100 }, false},
		{"request URL credential", func(c *Config) { c.Providers[0].RequestURL = "https://new.invalid?token=secret" }, false},
		{"legacy URL credential", func(c *Config) { c.Providers[0].ChatURL = "https://user:secret@new.invalid" }, false},
		{"auth mode changed", func(c *Config) { c.Providers[0].AuthHeader = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MODEL_CONTINUATION_OLD", "old-key")
			t.Setenv("MODEL_CONTINUATION_NEW", "new-key")
			t.Setenv("MODEL_CONTINUATION_EMPTY", "")
			fixture := func() *Config {
				c := &Config{Providers: []ProviderEntry{{Name: "p", Kind: "openai", BaseURL: "https://old.invalid", Model: "m", APIKeyEnv: "MODEL_CONTINUATION_OLD"}}}
				c.Agent.MaxSubagentDepth = 4
				return c
			}
			old, current := fixture(), fixture()
			tc.edit(current)
			old.Providers[0].resolvedAPIKey = "old-key"
			if len(current.Providers) > 0 {
				switch current.Providers[0].APIKeyEnv {
				case "MODEL_CONTINUATION_OLD":
					current.Providers[0].resolvedAPIKey = "old-key"
				case "MODEL_CONTINUATION_NEW":
					current.Providers[0].resolvedAPIKey = "new-key"
				}
				current.Providers[0].credentialsFrozen = true
			}
			if err := ValidateModelRuntimeContinuation(old, current); (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
		})
	}
}
