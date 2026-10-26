package boot

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/netclient"
)

// Config can contain proxy passwords, headers, and service tokens. Keep its
// fingerprint process-local and keyed rather than exposing a password oracle.
var runtimeConfigKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return key
}()

// The extension graph omits host configuration. Its no-op/narrow plans may
// reuse a controller only when the effective configuration still matches the
// one that built it, including project overrides and caller-owned snapshots.
func reusableRuntimeConfiguration(previous *BuildResult, opts Options) (*config.Config, bool, error) {
	if previous.configFingerprint == "" || previous.liveSettings == nil {
		return nil, false, nil
	}
	root := ResolveWorkspaceRoot(opts.WorkspaceRoot)
	cfg, resolved, err := resolveBuildSelection(root, opts)
	if err != nil {
		return nil, true, err
	}
	selection, selectionErr := runtimeSelectionForConfig(previous, cfg, resolved)
	if selectionErr != nil || previous.selection != selection {
		return nil, false, nil
	}
	fingerprint := runtimeConfigFingerprint(cfg, root)
	return cfg, fingerprint != "" && fingerprint == previous.configFingerprint, nil
}

func withRuntimeConfiguration(res *BuildResult, cfg *config.Config, live *liveRuntimeSettings, opts Options) *BuildResult {
	res.configFingerprint = runtimeConfigFingerprint(cfg, res.Controller.WorkspaceRoot())
	res.liveSettings, res.selection = live, runtimeSelectionFrom(opts)
	res.selection.model = res.Controller.ModelRef()
	return res
}

// Only assembly dependencies participate: frontend/transport preferences do not
// own this controller, and compaction thresholds have their own live setting.
type runtimeConfigDependencies struct {
	DefaultModel, ModelAccess, ResponseLanguage, DisplayCurrency string
	SystemPrompt                                                 string
	Agent                                                        config.AgentConfig
	Providers                                                    []runtimeProviderMetadata
	Tools                                                        config.ToolsConfig
	Checkpoints                                                  config.CheckpointsConfig
	Permissions                                                  config.PermissionsConfig
	Sandbox                                                      config.SandboxConfig
	Proxy                                                        netclient.ProxySpec
	Environment                                                  config.EnvironmentConfig
	Plugins                                                      []config.PluginEntry
	Skills                                                       config.SkillsConfig
	LSP                                                          config.LSPConfig
	Browser                                                      config.BrowserConfig
	Secrets                                                      config.SecretsConfig
	CredentialEnvNames                                           []string
	PluginOwners                                                 map[string]string
	SkillOwners, AgentOwners                                     map[string][]string
}

type runtimeProviderMetadata struct {
	BalanceURL, APIKeyEnv, CredentialProxyURL string
	Authentication                            control.AuthenticationState
}

func runtimeConfigFingerprint(cfg *config.Config, roots ...string) string {
	if cfg == nil {
		return ""
	}
	root := "."
	if len(roots) > 0 {
		root = roots[0]
	}
	prompt, err := cfg.ResolveSystemPromptForRoot(root)
	if err != nil {
		return "" // Let full assembly report the ordinary prompt resolution error.
	}
	modelAccess := cfg.ModelRuntimeFingerprint("")
	if modelAccess == "" {
		return ""
	}
	deps := projectRuntimeConfig(cfg, root)
	deps.SystemPrompt, deps.ModelAccess = prompt, modelAccess
	data, err := json.Marshal(deps)
	if err != nil {
		return "" // An unrepresentable snapshot must never authorize reuse.
	}
	mac := hmac.New(sha256.New, runtimeConfigKey)
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

func projectRuntimeConfig(cfg *config.Config, root string) runtimeConfigDependencies {
	// ModelRuntimeFingerprint owns provider credentials, access, role models and
	// effort/concurrency settings. Keep only the remaining captured agent options.
	a := cfg.Agent
	deps := runtimeConfigDependencies{
		DefaultModel: cfg.DefaultModel, ResponseLanguage: cfg.ResponseLanguage(),
		DisplayCurrency: cfg.ExplicitDisplayCurrency(),
		Agent: config.AgentConfig{
			Temperature: a.Temperature, GuardianTemperature: a.GuardianTemperature,
			TaskCostBudget: a.TaskCostBudget, TaskTimeBudgetMinutes: a.TaskTimeBudgetMinutes,
			GoalTokenBudget: a.GoalTokenBudget, OutputStyle: a.OutputStyle,
			ReasoningLanguage: cfg.ReasoningLanguage(), ColdResumePrune: a.ColdResumePrune,
			Keep: a.Keep, RecentKeep: a.RecentKeep, PlanModeReadOnlyCommands: a.PlanModeReadOnlyCommands,
			SoftCompactRatio: a.SoftCompactRatio, ToolResultSnipRatio: a.ToolResultSnipRatio,
			CompactForceRatio: a.CompactForceRatio, ContextEditing: a.ContextEditing,
		},
		Tools: cfg.Tools, Checkpoints: cfg.Checkpoints, Permissions: cfg.Permissions,
		Sandbox: config.SandboxConfig{
			WorkspaceRoot: cfg.WriteRootsForRoot(root)[0], AllowWrite: cfg.AllowWriteRoots(),
			ForbidRead: cfg.ForbidReadRootsForRoot(root), Bash: cfg.BashMode(), Network: cfg.Sandbox.Network,
		},
		Proxy: cfg.NetworkProxySpec(), Environment: cfg.Environment, Plugins: cfg.Plugins,
		Skills: config.SkillsConfig{
			Paths: cfg.SkillCustomPaths(), ExcludedPaths: cfg.SkillExcludedPaths(),
			DisabledSkills: cfg.DisabledSkillNames(), MaxDepth: cfg.SkillMaxDepth(),
			DisableImplicitInvocation: !cfg.ImplicitSkillInvocationEnabled(),
		},
		LSP: cfg.LSP, Browser: cfg.Browser, Secrets: cfg.Secrets,
		CredentialEnvNames: cfg.CredentialEnvNames(), PluginOwners: pluginPackageOwners(cfg),
		SkillOwners: cfg.PluginPackageSkillOwners(), AgentOwners: cfg.PluginPackageAgentOwners(),
	}
	// These are deliberately outside the model digest, but boot captures them for
	// balance lookups, missing-credential diagnostics and subprocess protection.
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		deps.Providers = append(deps.Providers, runtimeProviderMetadata{
			BalanceURL: p.BalanceURL, APIKeyEnv: p.APIKeyEnv, CredentialProxyURL: p.CredentialProxyURL(),
			Authentication: authenticationStateForModelEntry(p, ""),
		})
	}
	return deps
}
