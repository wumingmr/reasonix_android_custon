package boot

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
)

func projectionConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Providers = []config.ProviderEntry{{
		Name: "fixture", Kind: "openai", Model: "chat", BaseURL: "https://example.invalid",
		APIKeyEnv: "REASONIX_PROJECTION_TEST_KEY",
	}}
	cfg.DefaultModel = "fixture/chat"
	cfg.Agent.CompactRatio = .85
	cfg.FreezeProviderCredentials()
	return cfg
}

func TestRuntimeConfigFingerprintIgnoresPresentation(t *testing.T) {
	isolateConfigHome(t)
	t.Setenv("REASONIX_PROJECTION_TEST_KEY", "fixture-key")
	cases := []struct {
		name string
		edit func(*config.Config)
	}{
		{"compact ratio", func(c *config.Config) { c.Agent.CompactRatio = .8 }},
		{"CLI appearance", func(c *config.Config) { c.UI.Theme = "light" }},
		{"desktop appearance", func(c *config.Config) { c.Desktop.Theme = "light" }},
		{"desktop language", func(c *config.Config) { c.Desktop.Language = "zh" }},
		{"desktop defaults", func(c *config.Config) { c.Desktop.DefaultToolApprovalMode = "read-only" }},
		{"notifications", func(c *config.Config) { c.Notifications.Enabled = !c.Notifications.Enabled }},
		{"telemetry", func(c *config.Config) { c.Telemetry.CLIMetrics = "off" }},
		{"status line", func(c *config.Config) { c.Statusline.Command = "printf test" }},
		{"CLI updates", func(c *config.Config) { c.CLI.UpdateChannel = "test" }},
		{"bot transport", func(c *config.Config) { c.Bot.QueueCap++ }},
		{"serve transport", func(c *config.Config) { c.Serve.BehindProxy = !c.Serve.BehindProxy }},
		{"provider label", func(c *config.Config) { c.Providers[0].DisplayName = "Renamed" }},
		{"provider preset", func(c *config.Config) { c.Providers[0].PresetID = "preset"; c.Providers[0].PresetVersion++ }},
		{"model discovery URL", func(c *config.Config) { c.Providers[0].ModelsURL = "https://example.invalid/models" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := projectionConfig(t)
			before := runtimeConfigFingerprint(cfg)
			tc.edit(cfg)
			if after := runtimeConfigFingerprint(cfg); before == "" || after != before {
				t.Fatal("presentation or live-only change invalidated cached runtime dependencies")
			}
		})
	}
}

func TestRuntimeConfigFingerprintRetainsRuntimeDependencies(t *testing.T) {
	isolateConfigHome(t)
	t.Setenv("REASONIX_PROJECTION_TEST_KEY", "fixture-key")
	cases := []struct {
		name string
		edit func(*config.Config)
	}{
		{"reply language", func(c *config.Config) { c.Language = "zh" }},
		{"prompt", func(c *config.Config) { c.Agent.SystemPrompt += " extra" }},
		{"temperature", func(c *config.Config) { c.Agent.Temperature++ }},
		{"task budget", func(c *config.Config) { c.Agent.TaskCostBudget++ }},
		{"goal budget", func(c *config.Config) { c.Agent.GoalTokenBudget++ }},
		{"keep policy", func(c *config.Config) { c.Agent.Keep = []string{"all"} }},
		{"recent keep", func(c *config.Config) { c.Agent.RecentKeep++ }},
		{"plan command policy", func(c *config.Config) { c.Agent.PlanModeReadOnlyCommands = []string{"git status"} }},
		{"planner model", func(c *config.Config) { c.Agent.PlannerModel = "fixture/planner" }},
		{"child model", func(c *config.Config) { c.Agent.SubagentModel = "fixture/worker" }},
		{"provider access", func(c *config.Config) { c.Desktop.ProviderAccess = []string{} }},
		{"provider endpoint", func(c *config.Config) { c.Providers[0].BaseURL += "/v2" }},
		{"provider limits", func(c *config.Config) { c.Providers[0].ContextWindow++ }},
		{"provider header", func(c *config.Config) { c.Providers[0].Headers = map[string]string{"X-Test": "fixture"} }},
		{"balance endpoint", func(c *config.Config) { c.Providers[0].BalanceURL = "https://example.invalid/balance" }},
		{"credential name", func(c *config.Config) { c.Providers[0].APIKeyEnv = "OTHER_KEY" }},
		{"display currency", func(c *config.Config) { c.Billing.DisplayCurrency = "CNY" }},
		{"legacy currency", func(c *config.Config) { c.Billing.DisplayCurrency = ""; c.Desktop.Currency = "CNY" }},
		{"enabled tools", func(c *config.Config) { c.Tools.Enabled = []string{"read_file"} }},
		{"shell", func(c *config.Config) { c.Tools.Shell.Path = "/fixture/shell" }},
		{"permission rules", func(c *config.Config) { c.Permissions.Deny = []string{"bash"} }},
		{"sandbox roots", func(c *config.Config) { c.Sandbox.AllowWrite = []string{"/fixture/write"} }},
		{"sandbox reads", func(c *config.Config) { c.Sandbox.ForbidRead = []string{"/fixture/secret"} }},
		{"sandbox network", func(c *config.Config) { c.Sandbox.Network = !c.Sandbox.Network }},
		{"subprocess filtering", func(c *config.Config) { c.Secrets.FilterSubprocessEnv = !c.Secrets.FilterSubprocessEnv }},
		{"sensitive files", func(c *config.Config) { c.Secrets.ProtectSensitiveFiles = !c.Secrets.ProtectSensitiveFiles }},
		{"bot credential protection", func(c *config.Config) { c.Bot.QQ.AppSecretEnv = "BOT_FIXTURE_SECRET" }},
		{"proxy", func(c *config.Config) { c.Network.ProxyMode = "off" }},
		{"offline tools", func(c *config.Config) { c.Environment.Offline = !c.Environment.Offline }},
		{"skills", func(c *config.Config) { c.Skills.DisabledSkills = []string{"fixture"} }},
		{"plugins", func(c *config.Config) { c.Plugins = []config.PluginEntry{{Name: "fixture", Command: "fixture"}} }},
		{"language servers", func(c *config.Config) { c.LSP.Enabled = !c.LSP.Enabled }},
		{"browser", func(c *config.Config) { c.Browser.Enabled = !c.Browser.Enabled }},
		{"checkpoint retention", func(c *config.Config) { c.Checkpoints.RetainTurns++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := projectionConfig(t)
			before := runtimeConfigFingerprint(cfg)
			tc.edit(cfg)
			if after := runtimeConfigFingerprint(cfg); before == "" || after == "" || after == before {
				t.Fatal("changed cached runtime dependency did not invalidate reuse")
			}
		})
	}
}

func TestRuntimeConfigFingerprintRetainsFrozenCredentials(t *testing.T) {
	isolateConfigHome(t)
	t.Setenv("REASONIX_HOME", t.TempDir())
	writeKey := func(value string) {
		t.Helper()
		if err := os.WriteFile(config.UserCredentialsPath(), []byte("REASONIX_PROJECTION_TEST_KEY="+value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeKey("fixture-key")
	old := projectionConfig(t)
	before := runtimeConfigFingerprint(old)
	writeKey("rotated-fixture-key")
	if runtimeConfigFingerprint(old) != before {
		t.Fatal("frozen runtime credential changed with the credential store")
	}
	if runtimeConfigFingerprint(projectionConfig(t)) == before {
		t.Fatal("credential rotation did not invalidate runtime reuse")
	}
	writeKey("")
	if runtimeConfigFingerprint(projectionConfig(t)) == before {
		t.Fatal("credential removal did not invalidate runtime reuse")
	}
}

func TestRuntimeConfigFingerprintFailsClosed(t *testing.T) {
	isolateConfigHome(t)
	cfg := projectionConfig(t)
	cfg.Providers[0].ExtraBody = map[string]any{"unsupported": math.NaN()}
	if got := runtimeConfigFingerprint(cfg); got != "" {
		t.Fatal("unrepresentable provider settings authorized a reusable fingerprint")
	}
	if got := runtimeConfigFingerprint(nil); got != "" {
		t.Fatal("nil configuration authorized a reusable fingerprint")
	}
}

func TestRuntimeConfigFingerprintIncludesExpandedDependencies(t *testing.T) {
	isolateConfigHome(t)
	cases := []struct {
		name string
		edit func(*config.Config)
	}{
		{"proxy", func(c *config.Config) { c.Network.Proxy.Password = "${REASONIX_PROJECTION_VALUE}" }},
		{"write roots", func(c *config.Config) { c.Sandbox.AllowWrite = []string{"${REASONIX_PROJECTION_VALUE}"} }},
		{"read roots", func(c *config.Config) { c.Sandbox.ForbidRead = []string{"${REASONIX_PROJECTION_VALUE}"} }},
		{"skill roots", func(c *config.Config) { c.Skills.Paths = []string{"${REASONIX_PROJECTION_VALUE}"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := projectionConfig(t)
			tc.edit(cfg)
			t.Setenv("REASONIX_PROJECTION_VALUE", "/fixture/first")
			before := runtimeConfigFingerprint(cfg)
			t.Setenv("REASONIX_PROJECTION_VALUE", "/fixture/second")
			if after := runtimeConfigFingerprint(cfg); before == "" || after == "" || after == before {
				t.Fatal("changed expanded dependency did not invalidate runtime reuse")
			}
		})
	}
}

func TestRuntimeConfigFingerprintUsesWorkspacePrompt(t *testing.T) {
	isolateConfigHome(t)
	root := t.TempDir()
	cfg := projectionConfig(t)
	cfg.Agent.SystemPromptFile = "prompt.txt"
	path := filepath.Join(root, cfg.Agent.SystemPromptFile)
	if got := runtimeConfigFingerprint(cfg, root); got != "" {
		t.Fatal("missing prompt file authorized runtime reuse")
	}
	if err := os.WriteFile(path, []byte("first prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := runtimeConfigFingerprint(cfg, root)
	if err := os.WriteFile(path, []byte("second prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if after := runtimeConfigFingerprint(cfg, root); before == "" || after == "" || after == before {
		t.Fatal("changed workspace prompt did not invalidate runtime reuse")
	}
}

func TestRuntimeConfigFingerprintIncludesCredentialProxy(t *testing.T) {
	isolateConfigHome(t)
	root := t.TempDir()
	cfg := projectionConfig(t)
	settings := config.ModelRuntimeSettings{
		ProxyURL: "http://127.0.0.1:1234", Providers: cfg.Providers,
		Credentials: map[string]string{"fixture": "fixture-tunnel-token"},
	}
	if err := settings.Apply(cfg, root); err != nil {
		t.Fatal(err)
	}
	before := runtimeConfigFingerprint(cfg, root)
	settings.ProxyURL = "http://127.0.0.1:1235"
	if err := settings.Apply(cfg, root); err != nil {
		t.Fatal(err)
	}
	if after := runtimeConfigFingerprint(cfg, root); before == "" || after == "" || after == before {
		t.Fatal("changed private credential proxy route did not invalidate runtime reuse")
	}
}
