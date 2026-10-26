package boot

import "reasonix/internal/config"

func runtimeConfigHostClassification() []configTypeClassification {
	return []configTypeClassification{
		// Upload admission reads this policy per request, outside controller assembly.
		classifyConfigType[config.AttachmentsConfig](configFieldClasses{excluded: "MaxStreamBytes"}),
		classifyConfigType[config.DesktopConfig](configFieldClasses{
			runtime: "Currency ProviderAccess",
			excluded: "Language LayoutStyle Theme ThemeStyle TerminalTheme ExternalOpener CloseBehavior DisplayMode " +
				"StatusBarStyle StatusBarStyleInitialized StatusBarItems DefaultToolApprovalMode CheckUpdates " +
				"UpdateChannel Telemetry Metrics SessionExperience ExpandThinking ReasoningDisplayMode ConversationWidth",
		}),
		classifyConfigType[config.UIConfig](configFieldClasses{
			excluded: "Theme ThemeStyle ShortcutLayout CloseBehavior ShowReasoning ShowTurnUsage CursorShape",
		}),
		classifyConfigType[config.CLIConfig](configFieldClasses{excluded: "UpdateChannel"}),
		classifyConfigType[config.TelemetryConfig](configFieldClasses{excluded: "CLIMetrics"}),
		classifyConfigType[config.NotificationsConfig](configFieldClasses{excluded: "Enabled TurnDone ApprovalRequest AskRequest"}),
		classifyConfigType[config.StatuslineConfig](configFieldClasses{excluded: "Command"}),
		// These host-owned transports do not own the assembled chat controller.
		// Credential variable names remain runtime inputs to subprocess protection.
		classifyConfigType[config.BotConfig](configFieldClasses{
			runtime: "QQ Feishu Weixin Connections",
			excluded: "Enabled Model ToolApprovalMode MaxSteps DebounceMs QueueMode QueueCap QueueDrop " +
				"IgnoreSelfMessages SelfUserIDs Control Pairing Allowlist Dingtalk Routes DesktopWatchers",
		}),
		classifyConfigType[config.BotDesktopWatcherConfig](configFieldClasses{excluded: "Platform ConnectionID Domain ChatType ChatID"}),
		classifyConfigType[config.BotSelfUserIDs](configFieldClasses{excluded: "QQ Feishu Weixin Dingtalk"}),
		classifyConfigType[config.BotControlConfig](configFieldClasses{excluded: "Enabled Addr TokenEnv"}),
		classifyConfigType[config.BotRouteConfig](configFieldClasses{
			excluded: "ConnectionID Platform ChatType ChatID UserID ThreadID Model ToolApprovalMode WorkspaceRoot",
		}),
		classifyConfigType[config.BotAllowlist](configFieldClasses{
			excluded: "Enabled AllowAll QQUsers FeishuUsers WeixinUsers QQApprovers FeishuApprovers WeixinApprovers " +
				"QQAdmins FeishuAdmins WeixinAdmins QQGroups FeishuGroups WeixinGroups " +
				"DingtalkUsers DingtalkApprovers DingtalkAdmins DingtalkGroups",
		}),
		classifyConfigType[config.BotPairingConfig](configFieldClasses{excluded: "Enabled RequestTTLMinutes MaxPendingPerPlatform"}),
		classifyConfigType[config.BotAccessConfig](configFieldClasses{excluded: "Enabled AllowAll PairingEnabled Users Groups Approvers Admins"}),
		classifyConfigType[config.QQBotConfig](configFieldClasses{
			runtime: "AppSecretEnv", excluded: "Enabled AppID Sandbox Model ToolApprovalMode WorkspaceRoot Access",
		}),
		classifyConfigType[config.FeishuBotConfig](configFieldClasses{
			runtime:  "AppSecretEnv",
			excluded: "Enabled Domain AppID VerificationToken Mode WebhookPort RequireMention OutboundMediaRoots",
		}),
		classifyConfigType[config.WeixinBotConfig](configFieldClasses{runtime: "TokenEnv", excluded: "Enabled AccountID APIBase"}),
		classifyConfigType[config.DingtalkBotConfig](configFieldClasses{
			excluded: "Enabled ClientID ClientSecret ClientIDEnv SecretEnv BotName RequireMention Model ToolApprovalMode " +
				"WorkspaceRoot Access SessionMappings",
		}),
		classifyConfigType[config.BotConnectionConfig](configFieldClasses{
			runtime: "Credential",
			excluded: "ID Provider Domain Label Enabled Status Model ToolApprovalMode WorkspaceRoot Access " +
				"SessionMappings LastError CreatedAt UpdatedAt",
		}),
		classifyConfigType[config.BotConnectionCredential](configFieldClasses{
			runtime: "AppSecretEnv TokenEnv", excluded: "AppID AccountID",
		}),
		classifyConfigType[config.BotConnectionSessionMapping](configFieldClasses{
			excluded: "RemoteID SessionID SessionSource ChatType UserID ThreadID Scope WorkspaceRoot UpdatedAt",
		}),
		classifyConfigType[config.ServeConfig](configFieldClasses{excluded: "AuthMode Token PasswordHash BehindProxy"}),
		classifyConfigType[config.RemoteConfig](configFieldClasses{runtime: "Hosts", excluded: "ImportSSHConfig Projects"}),
		classifyConfigType[config.RemoteHostEntry](configFieldClasses{
			runtime:  "PassphraseEnv PasswordEnv",
			excluded: "Name Host Port User IdentityFile ProxyJump Workspace ServeInstall CredentialMode UseSSHConfig Forwards",
		}),
		classifyConfigType[config.RemoteForwardEntry](configFieldClasses{excluded: "Type Bind Target"}),
		classifyConfigType[config.RemoteProjectEntry](configFieldClasses{excluded: "HostID Workspace Title SessionOrganization"}),
	}
}
