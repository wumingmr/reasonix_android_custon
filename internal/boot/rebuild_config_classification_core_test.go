package boot

import (
	"reasonix/internal/config"
	"reasonix/internal/provider"
)

func runtimeConfigCoreClassification() []configTypeClassification {
	return []configTypeClassification{
		classifyConfigType[config.Config](configFieldClasses{
			runtime: "DefaultModel Language CredentialsStore Desktop Billing Agent Providers Tools Checkpoints " +
				"Permissions Sandbox Network Environment Plugins Skills LSP Browser Bot Secrets Remote",
			excluded: "ConfigVersion UI CLI Telemetry Notifications Statusline Serve Attachments",
		}),
		classifyConfigType[config.AgentConfig](configFieldClasses{
			runtime: "SystemPrompt SystemPromptFile Temperature PlannerModel WebSearchModel VisionModel " +
				"GuardianModel GuardianTemperature RecoveryModel SubagentModel SubagentModels SubagentEffort " +
				"SubagentEfforts MaxSubagentDepth TaskCostBudget TaskTimeBudgetMinutes GoalTokenBudget " +
				"MaxSubagentConcurrency MaxParallelWriters OutputStyle ReasoningLanguage SoftCompactRatio " +
				"ToolResultSnipRatio CompactForceRatio ContextEditing Keep RecentKeep ColdResumePrune PlanModeReadOnlyCommands",
			live: "CompactRatio",
			excluded: "MaxSteps PlannerMaxSteps RecoveryTemperature AutoPlan AutoPlanClassifier " +
				"LegacyAnchorSafetyGate CompletionValidation CompletionEvaluatorModel",
		}),
		// RecoveryModel and retired compaction fields still participate in the model
		// digest/runtime projection. Removing their entries requires deliberate review.
		classifyConfigType[config.ProviderEntry](configFieldClasses{
			runtime: "Name Kind BaseURL ChatURL RequestURL Model Models Default APIKeyEnv Headers ExtraBody " +
				"AuthHeader ResponsesMode ResponsesStateful BalanceURL ContextWindow MaxOutputTokens Price Prices " +
				"BillingCurrency BillingMode Thinking Effort Vision VisionModels VisionDetail WebSearch ReasoningProtocol " +
				"SupportedEfforts DefaultEffort ModelOverrides NoProxy CacheTTLMinutes",
			excluded: "DisplayName ModelsURL PresetID PresetVersion ReasoningMetadataUnknown",
		}),
		// ReasoningMetadataUnknown is transient resolver metadata with json:"-";
		// unlike new exported fields, it is explicitly reviewed rather than skipped.
		classifyConfigType[config.ProviderModelOverride](configFieldClasses{
			runtime: "ReasoningDefaults ReasoningProtocol SupportedEfforts DefaultEffort Vision ContextWindow MaxOutputTokens",
		}),
		classifyConfigType[provider.Pricing](configFieldClasses{runtime: "CacheHit Input Output Currency"}),
		classifyConfigType[config.ToolsConfig](configFieldClasses{
			runtime: "Enabled BashTimeoutSeconds MCPStartupTimeoutSeconds MCPCallTimeoutSeconds BackgroundJobs Search Shell",
		}),
		classifyConfigType[config.BackgroundJobsConfig](configFieldClasses{runtime: "StalledWarningSeconds"}),
		classifyConfigType[config.SearchConfig](configFieldClasses{runtime: "Engine RgPath"}),
		classifyConfigType[config.ShellConfig](configFieldClasses{runtime: "Prefer Path"}),
		classifyConfigType[config.CheckpointsConfig](configFieldClasses{runtime: "RetainTurns BlobQuotaBytes"}),
		classifyConfigType[config.PermissionsConfig](configFieldClasses{runtime: "Mode Allow Ask Deny AllowDynamicBash"}),
		classifyConfigType[config.SandboxConfig](configFieldClasses{runtime: "WorkspaceRoot AllowWrite ForbidRead Bash Network"}),
		classifyConfigType[config.NetworkConfig](configFieldClasses{runtime: "ProxyMode ProxyURL NoProxy Proxy"}),
		classifyConfigType[config.NetworkProxyConfig](configFieldClasses{runtime: "Type Server Port Username Password"}),
		classifyConfigType[config.EnvironmentConfig](configFieldClasses{runtime: "Enabled Offline Tools"}),
		classifyConfigType[config.PluginEntry](configFieldClasses{
			runtime: "Name Type Command Args Env URL Headers StartupTimeoutSeconds CallTimeoutSeconds " +
				"ToolTimeoutSeconds Concurrency AutoStart Tier Source",
		}),
		// Source owns activation provenance even though JSON omits it. Similarly,
		// CredentialsStore affects resolved credentials rather than a direct digest term.
		classifyConfigType[config.SkillsConfig](configFieldClasses{
			runtime: "Paths ExcludedPaths DisabledSkills DisableImplicitInvocation MaxDepth",
		}),
		classifyConfigType[config.LSPConfig](configFieldClasses{runtime: "Enabled Servers"}),
		classifyConfigType[config.LSPServer](configFieldClasses{runtime: "Command Args Env LanguageID Extensions InstallHint"}),
		classifyConfigType[config.BrowserConfig](configFieldClasses{
			runtime: "Enabled Endpoint AllowRemoteEndpoint ChromePath ChromeArgs UserDataDir Headless",
		}),
		classifyConfigType[config.SecretsConfig](configFieldClasses{runtime: "FilterSubprocessEnv ProtectSensitiveFiles"}),
		classifyConfigType[config.BillingConfig](configFieldClasses{runtime: "DisplayCurrency"}),
	}
}
